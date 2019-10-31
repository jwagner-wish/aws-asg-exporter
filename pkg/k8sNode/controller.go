package k8sNode

import (
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	core_v1 "k8s.io/api/core/v1"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	listers_v1 "k8s.io/client-go/listers/core/v1"
)

// Controller calls onChange when the resource changes
type Controller struct {
	Clientset *kubernetes.Clientset
	informer  cache.Controller
	indexer   cache.Indexer
	lister    listers_v1.NodeLister
}

// Run starts the controller loop
func (c *Controller) Run(stopCh <-chan struct{}) {
	go c.informer.Run(stopCh)

	// Wait for the caches to be synced before starting workers
	logrus.Info("Waiting for initial cache sync")
	if ok := cache.WaitForCacheSync(stopCh, c.informer.HasSynced); !ok {
		logrus.Error("Failed to sync informer cache")
		return
	}
	logrus.Info("cache synced")
}

// NodeByName returns the node with the given name, or nil if it doesn't exist
func (c *Controller) NodeByName(name string) (*core_v1.Node, error) {
	nodeIface, exists, err := c.indexer.GetByKey(name)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return nodeIface.(*core_v1.Node), nil
}

// ListNodes returns every node in the cluster
func (c *Controller) ListNodes() ([]*core_v1.Node, error) {
	return c.lister.List(labels.Everything())
}

// NewController creates a controller that calls the given function on resource changes
func NewController() (*Controller, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	indexer, informer := cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(opts meta_v1.ListOptions) (runtime.Object, error) {
				return clientset.CoreV1().Nodes().List(opts)
			},
			WatchFunc: func(opts meta_v1.ListOptions) (watch.Interface, error) {
				return clientset.CoreV1().Nodes().Watch(opts)
			},
		},
		&core_v1.Node{},
		30*time.Second, // Do a full update every 30 seconds, making extra sure nothing was missed
		cache.ResourceEventHandlerFuncs{},
		cache.Indexers{},
	)

	lister := listers_v1.NewNodeLister(indexer)

	controller := Controller{
		clientset,
		informer,
		indexer,
		lister,
	}

	return &controller, nil
}
