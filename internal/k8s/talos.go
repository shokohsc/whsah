package k8s

import (
	"context"

	policyv1beta1 "k8s.io/api/policy/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

// Client manages Talos worker nodes via the Kubernetes API.
type Client struct {
	clientset kubernetes.Interface
}

func New(kubeconfig string) (*Client, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Client{clientset: clientset}, nil
}

// NewWithClientset builds a Client backed by an existing clientset (for tests).
func NewWithClientset(cs kubernetes.Interface) *Client {
	return &Client{clientset: cs}
}

// NewInCluster builds a Client using the in-cluster service account token.
func NewInCluster() (*Client, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	return &Client{clientset: clientset}, nil
}

// Cordon marks a node unschedulable.
func (c *Client) Cordon(node string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		n, err := c.clientset.CoreV1().Nodes().Get(context.Background(), node, metav1.GetOptions{})
		if err != nil {
			return err
		}
		n.Spec.Unschedulable = true
		_, err = c.clientset.CoreV1().Nodes().Update(context.Background(), n, metav1.UpdateOptions{})
		return err
	})
}

// Drain evicts all pods from a node (excluding DaemonSet-managed mirror/static pods).
func (c *Client) Drain(node string) error {
	pods, err := c.clientset.CoreV1().Pods(metav1.NamespaceAll).List(context.Background(), metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
	})
	if err != nil {
		return err
	}
	for _, p := range pods.Items {
		if p.Spec.NodeName != node {
			continue
		}
		_ = c.clientset.CoreV1().Pods(p.Namespace).Evict(context.Background(), &policyv1beta1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace},
		})
	}
	return nil
}

// Delete removes the Node object.
func (c *Client) Delete(node string) error {
	return c.clientset.CoreV1().Nodes().Delete(context.Background(), node, metav1.DeleteOptions{})
}
