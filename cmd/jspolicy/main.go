package main

import (
	"context"
	policyv1beta1 "github.com/loft-sh/jspolicy/pkg/apis/policy/v1beta1"
	"github.com/loft-sh/jspolicy/pkg/blockingcacheclient"
	"github.com/loft-sh/jspolicy/pkg/cache"
	"github.com/loft-sh/jspolicy/pkg/controller"
	"github.com/loft-sh/jspolicy/pkg/controllers"
	"github.com/loft-sh/jspolicy/pkg/leaderelection"
	"github.com/loft-sh/jspolicy/pkg/store/validatingwebhookconfiguration"
	"github.com/loft-sh/jspolicy/pkg/util/certhelper"
	"github.com/loft-sh/jspolicy/pkg/util/log"
	"github.com/loft-sh/jspolicy/pkg/util/secret"
	vm2 "github.com/loft-sh/jspolicy/pkg/vm"
	"github.com/loft-sh/jspolicy/pkg/vm/vmpool"
	"github.com/loft-sh/jspolicy/pkg/webhook"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsv1beta1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1beta1"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"math/rand"
	"net/http"
	"os"
	sigcache "sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	// +kubebuilder:scaffold:imports

	// Make sure dep tools picks up these dependencies
	_ "github.com/go-openapi/loads"
	_ "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // Enable cloud provider auth
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	VMPoolSize         = 4
	CacheCleanupPeriod = time.Hour * 3
	CacheResyncPeriod  = time.Hour * 6
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	// API extensions are not in the above scheme set,
	// and must thus be added separately.
	_ = apiextensionsv1beta1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	_ = policyv1beta1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme

	size := os.Getenv("VM_POOL_SIZE")
	if size != "" {
		sizeInt, err := strconv.Atoi(size)
		if err != nil {
			klog.Fatalf("Error converting VM_POOL_SIZE to number: %v", err)
		}

		VMPoolSize = sizeInt
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())

	// set global logger
	if os.Getenv("DEBUG") == "true" {
		ctrl.SetLogger(log.NewLog(0))
	} else {
		ctrl.SetLogger(log.NewLog(2))
	}

	// retrieve in cluster config
	config := ctrl.GetConfigOrDie()

	// set qps, burst & timeout
	config.QPS = 80
	config.Burst = 100
	config.Timeout = 0

	// create a new temporary client
	uncachedClient, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		setupLog.Error(err, "unable to create client")
		os.Exit(1)
	}

	// Make sure the certificates are there, this is safe with leader election
	err = secret.EnsureCertSecrets(context.Background(), uncachedClient)
	if err != nil {
		setupLog.Error(err, "unable to generate certificates")
		os.Exit(1)
	}

	// create the manager
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		NewClient: func(cache sigcache.Cache, config *rest.Config, options client.Options, uncachedObjects ...client.Object) (client.Client, error) {
			return blockingcacheclient.NewCacheClient(cache, config, options)
		},
		Scheme:             scheme,
		MetricsBindAddress: ":8080",
		CertDir:            certhelper.WebhookCertFolder,
		LeaderElection:     false,
		Port:               443,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Add required indices
	err = controllers.AddManagerIndices(mgr.GetCache())
	if err != nil {
		setupLog.Error(err, "unable to set manager indices")
		os.Exit(1)
	}

	stopChan := make(chan struct{})
	ctx := signals.SetupSignalHandler()

	// New cache
	cachedClient, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme:  mgr.GetScheme(),
		Mapper:  mgr.GetRESTMapper(),
		Resync:  &CacheResyncPeriod,
		Cleanup: &CacheCleanupPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to create cached client")
		os.Exit(1)
	}

	// Create the v8 vm pool
	vmPool, err := vmpool.NewVMPool(VMPoolSize, func() (vm2.VM, error) {
		return vm2.NewVM(cachedClient, uncachedClient, func(str string) {
			klog.Info(str)
		})
	})
	if err != nil {
		setupLog.Error(err, "unable to create vm pool")
		os.Exit(1)
	}

	// create controller policy manager
	controllerPolicyManager := controller.NewControllerPolicyManager(mgr, vmPool, cachedClient)

	// Register webhooks
	err = webhook.Register(mgr, vmPool)
	if err != nil {
		setupLog.Error(err, "unable to register webhooks")
		os.Exit(1)
	}

	// Start the local manager
	go func() {
		setupLog.Info("starting manager")
		err = mgr.Start(ctx)
		if err != nil {
			panic(err)
		}
	}()

	// Make sure the manager is synced
	mgr.GetCache().WaitForCacheSync(ctx)

	// start the controller policy manager (controller policies will be only executed if we are leader,
	// however the controller policy manager also takes care of garbage collecting the cache, so we need
	// to start it in any case)
	go func() {
		err := controllerPolicyManager.Start(ctx)
		if err != nil {
			klog.Fatalf("Error starting background policy manager: %v", err)
		}
	}()

	// start health check server
	go func() {
		// create a new handler
		handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {})

		// listen and serve
		_ = http.ListenAndServe(":80", handler)
	}()

	// start leader election for controllers
	go func() {
		err = leaderelection.StartLeaderElection(ctx, scheme, config, func() error {
			// setup ValidatingWebhookConfiguration
			if os.Getenv("UPDATE_WEBHOOK") != "false" {
				err = validatingwebhookconfiguration.EnsureValidatingWebhookConfiguration(context.Background(), mgr.GetClient())
				if err != nil {
					setupLog.Error(err, "unable to set up validating webhook configuration")
					os.Exit(1)
				}
			}

			// register controllers
			err := controllers.Register(mgr, controllerPolicyManager)
			if err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			klog.Fatalf("Error starting leader election: %v", err)
		}
	}()

	// Wait till stopChan is closed
	<-stopChan
}
