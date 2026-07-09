// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

//go:build kindrepro

// This is a manual reproduction for the panic in validateKubeJWT when a token
// review response has a non-nil Extra map that does not contain the pod-name
// key (e.g. a service account token that is not bound to a pod). Run it against
// a real cluster (e.g. kind):
//
//	kind create cluster --name eg-repro
//	KUBECONFIG=$(kind get kubeconfig-path --name eg-repro 2>/dev/null || echo ~/.kube/config) \
//	  go test -tags kindrepro -count=1 -run TestValidateKubeJWTNonPodBoundToken \
//	  ./internal/xds/server/kubejwt/ -v
//
// On the buggy code the test fails because validateKubeJWT panics with
// "index out of range [0] with length 0". After the fix it passes because the
// function returns an error instead of panicking.
package kubejwt

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestValidateKubeJWTNonPodBoundToken(t *testing.T) {
	const (
		ns       = "default"
		saName   = "eg-kubejwt-repro"
		audience = "eg-kubejwt-repro-audience"
		nodeID   = "some-node-id"
	)

	// Build a clientset from the local kubeconfig (KUBECONFIG env or ~/.kube/config).
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		kubeconfig = clientcmd.RecommendedHomeFile
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	require.NoError(t, err, "failed to load kubeconfig %q; point KUBECONFIG at your kind cluster", kubeconfig)
	cs, err := kubernetes.NewForConfig(cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Create a service account.
	_, err = cs.CoreV1().ServiceAccounts(ns).Create(ctx, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ns},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_ = cs.CoreV1().ServiceAccounts(ns).Delete(context.Background(), saName, metav1.DeleteOptions{})
	})

	// Mint a token for the SA that is NOT bound to a pod (no BoundObjectRef).
	// On modern Kubernetes the resulting TokenReview has a non-nil Extra map
	// (e.g. an authentication.kubernetes.io/credential-id entry) but no
	// authentication.kubernetes.io/pod-name entry.
	tr, err := cs.CoreV1().ServiceAccounts(ns).CreateToken(ctx, saName, &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			Audiences: []string{audience},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err, "failed to mint service account token")

	i := &JWTAuthInterceptor{
		clientset: cs,
		audience:  audience,
	}

	// The token is valid but not pod-bound. Correct behavior is to return an
	// error; the bug makes validateKubeJWT panic on podName[0].
	require.NotPanics(t, func() {
		if err := i.validateKubeJWT(ctx, tr.Status.Token, nodeID); err != nil {
			t.Logf("validateKubeJWT returned error (expected after fix): %v", err)
		}
	}, "validateKubeJWT must not panic on a non-pod-bound service account token")
}
