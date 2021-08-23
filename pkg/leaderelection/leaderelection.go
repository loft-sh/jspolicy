package leaderelection

import (
	"context"
	"github.com/loft-sh/jspolicy/pkg/util/clienthelper"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientv1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	"os"
	"time"
)

func StartLeaderElection(ctx context.Context, scheme *runtime.Scheme, restConfig *rest.Config, run func() error) error {
	// retrieve the current namespace
	namespace, err := clienthelper.CurrentNamespace()
	if err != nil {
		return errors.Wrap(err, "get current namespace")
	}

	// create the event recorder
	recorderClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return errors.Wrap(err, "create kubernetes client")
	}
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(func(format string, args ...interface{}) { klog.Infof(format, args...) })
	eventBroadcaster.StartRecordingToSink(&clientv1.EventSinkImpl{Interface: recorderClient.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme, corev1.EventSource{Component: "jspolicy"})

	// create the leader election client
	leaderElectionClient, err := kubernetes.NewForConfig(rest.AddUserAgent(restConfig, "leader-election"))
	if err != nil {
		return errors.Wrap(err, "create leader election client")
	}

	// Identity used to distinguish between multiple controller instances
	id, err := os.Hostname()
	if err != nil {
		return err
	}

	// Lock required for leader election
	rl := resourcelock.ConfigMapLock{
		ConfigMapMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      "jspolicy-controller",
		},
		Client: leaderElectionClient.CoreV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity:      id + "-external-jspolicy-controller",
			EventRecorder: recorder,
		},
	}

	// try and become the leader and start controller manager loops
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:          &rl,
		LeaseDuration: time.Duration(60) * time.Second,
		RenewDeadline: time.Duration(40) * time.Second,
		RetryPeriod:   time.Duration(15) * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Info("Acquired leadership and run jspolicy in leader mode")

				// start jspolicy in leader mode
				err = run()
				if err != nil {
					klog.Fatal(err)
				}
			},
			OnStoppedLeading: func() {
				klog.Info("leader election lost")
				os.Exit(1)
			},
		},
	})

	return nil
}
