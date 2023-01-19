/*
Copyright 2023.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	healv1alpha1 "gitlab.myteksi.net/projects/dakota/sre/redeem/api/v1alpha1"
)

// ReloadReconciler reconciles a Reload object
type ReloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=heal.gxs.com,resources=reloads,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=heal.gxs.com,resources=reloads/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=heal.gxs.com,resources=reloads/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Reload object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.13.0/pkg/reconcile
func (r *ReloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)
	reload := &healv1alpha1.Reload{}
	err := r.Get(ctx, req.NamespacedName, reload)
	// TODO(user): your logic here

	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then, it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			log.Info("reload resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get reload")
		return ctrl.Result{}, err
	}
	podList := &v1.PodList{}
	opts := []client.ListOption{
		client.InNamespace(req.NamespacedName.Namespace),
		//client.MatchingFields{"status.phase": "Running"},
	}
	err = r.List(ctx, podList, opts...)
	if err != nil {
		panic(err.Error())
	}
	fmt.Printf("There are %d pods in the cluster, %s namespace\n", len(podList.Items), req.NamespacedName.Namespace)
	for _, pod := range podList.Items {
		fmt.Println(pod.Name, pod.Status.Phase, pod.OwnerReferences)
		str, ismatch := getPodLogs(pod.Name, req.Namespace, reload.Spec.ContainerName, reload.Spec.Logmsg)
		if ismatch {
			fmt.Println(str)
			depName, err := getDeploymentName(pod.OwnerReferences, req.Namespace)
			if err != nil {
				log.Info("Pod does not belong to any deployment! ", pod.Name)
			} else {
				restartDeployment(depName, req.Namespace)
			}

		}
	}
	return ctrl.Result{RequeueAfter: time.Second * 30, Requeue: true}, nil
}
func getReplicaName(podReference []metav1.OwnerReference) (string, error) {
	for _, j := range podReference {
		if j.Kind == "ReplicaSet" {
			fmt.Println("Replicaset name is ", j.Name)
			return j.Name, nil
		}
	}
	return "", fmt.Errorf("not found")

}

func getDeploymentName(podReference []metav1.OwnerReference, podNamespace string) (string, error) {
	clientset, err := kubernetes.NewForConfig(GetKubeConfig("DEV"))
	if err != nil {
		fmt.Println("Error while retrieving k8s client", err)
	}
	opts := metav1.GetOptions{}
	rsName, err := getReplicaName(podReference)
	if err == nil {
		rsOwnerRef, _ := clientset.AppsV1().ReplicaSets(podNamespace).Get(context.TODO(), rsName, opts)
		fmt.Println("Replicaset reference", rsOwnerRef.OwnerReferences)
		for _, j := range rsOwnerRef.OwnerReferences {
			if j.Kind == "Deployment" {
				fmt.Println("Deployment name is ", j.Name)
				return j.Name, nil
			}
		}

	}
	return "ok", fmt.Errorf("not found")
}

func restartDeployment(depName string, podNamespace string) {
	clientset, err := kubernetes.NewForConfig(GetKubeConfig("DEV"))
	if err != nil {
		fmt.Println("Error while retrieving k8s client", err)
	}
	deploymentsClient := clientset.AppsV1().Deployments(podNamespace)
	data := fmt.Sprintf(`{"spec": {"template": {"metadata": {"annotations": {"kubectl.kubernetes.io/restartedAt": "%s"}}}}}`, time.Now().Format("20060102150405"))
	deployment, err := deploymentsClient.Patch(context.TODO(), depName, k8stypes.StrategicMergePatchType, []byte(data), metav1.PatchOptions{})
	if err != nil {
		fmt.Println("Error restarting deployment", err)
	} else {
		fmt.Println("Deployment was restarted successfully " + deployment.Name)
	}
}

func getPodLogs(podName string, podNamespace string, containerName string, matchLog string) (string, bool) {
	count := int64(350)
	clientset, err := kubernetes.NewForConfig(GetKubeConfig("DEV"))
	if err != nil {
		fmt.Println("Error while retrieving k8s client", err)
	}
	podLogOpts := v1.PodLogOptions{
		Container:    containerName,
		SinceSeconds: &count,
	}

	req := clientset.CoreV1().Pods(podNamespace).GetLogs(podName, &podLogOpts)
	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		return "error in opening stream" + err.Error(), false
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, podLogs)
	if err != nil {
		return "error in copy information from podLogs to buf", false
	}
	str := buf.String()
	match, _ := regexp.MatchString(matchLog, str)
	return str, match
}

func GetKubeConfig(mode string) *rest.Config {
	config := &rest.Config{}
	if mode == "DEV" {
		//Outside k8s Cluster authentication
		var kubeconfig string
		if home := homedir.HomeDir(); home != "" {
			//kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
			kubeconfig = filepath.Join(home, ".kube", "config")
		} else {
			//kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
			kubeconfig = ""
		}
		//flag.Parse()
		// use the current context in kubeconfig
		config, _ = clientcmd.BuildConfigFromFlags("", kubeconfig)
		//if err != nil {
		//	panic(err.Error())
		//}
	} else {
		config, _ = rest.InClusterConfig()
	}
	return config
}

// SetupWithManager sets up the controller with the Manager.
func (r *ReloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&healv1alpha1.Reload{}).
		Complete(r)
}
