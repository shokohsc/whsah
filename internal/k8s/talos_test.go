package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewInvalidKubeconfig(t *testing.T) {
	_, err := New("/nonexistent/kubeconfig")
	if err == nil {
		t.Fatal("expected error for nonexistent kubeconfig")
	}
}

func TestCordonDrainDelete(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	if _, err := cs.CoreV1().Nodes().Create(ctx, &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Spec:       corev1.NodeSpec{Unschedulable: false},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create node: %v", err)
	}

	c := NewWithClientset(cs)

	if err := c.Cordon("node-1"); err != nil {
		t.Fatalf("cordon: %v", err)
	}
	n, err := cs.CoreV1().Nodes().Get(ctx, "node-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !n.Spec.Unschedulable {
		t.Fatal("expected node to be unschedulable after cordon")
	}

	if err := c.Drain("node-1"); err != nil {
		t.Fatalf("drain: %v", err)
	}

	if err := c.Delete("node-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := cs.CoreV1().Nodes().Get(ctx, "node-1", metav1.GetOptions{}); err == nil {
		t.Fatal("expected node to be deleted")
	}
}
