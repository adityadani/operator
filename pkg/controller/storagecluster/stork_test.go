package storagecluster

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/hashicorp/go-version"
	_ "github.com/libopenstorage/operator/drivers/storage/portworx"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/libopenstorage/operator/pkg/util"
	testutil "github.com/libopenstorage/operator/pkg/util/test"
	"github.com/portworx/sched-ops/k8s"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakek8sclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	schedulerv1 "k8s.io/kubernetes/pkg/scheduler/api/v1"
)

func TestStorkInstallation(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Env: []v1.EnvVar{
					{
						Name:  "TEST",
						Value: "test-value",
					},
					{
						Name: "SECRET_ENV",
						ValueFrom: &v1.EnvVarSource{
							SecretKeyRef: &v1.SecretKeySelector{
								LocalObjectReference: v1.LocalObjectReference{
									Name: "secret-name",
								},
								Key: "secret-key",
							},
						},
					},
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	// Stork ConfigMap
	expectedPolicy, _ := json.Marshal(schedulerv1.Policy{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Policy",
			APIVersion: "v1",
		},
		ExtenderConfigs: []schedulerv1.ExtenderConfig{
			{
				URLPrefix:      "http://stork-service.kube-test:8099",
				FilterVerb:     "filter",
				PrioritizeVerb: "prioritize",
				Weight:         5,
			},
		},
	})
	storkConfigMap := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkConfigMap, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, storkConfigMapName, storkConfigMap.Name)
	require.Equal(t, cluster.Namespace, storkConfigMap.Namespace)
	require.Len(t, storkConfigMap.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkConfigMap.OwnerReferences[0].Name)
	require.Equal(t, string(expectedPolicy), storkConfigMap.Data["policy.cfg"])

	// ServiceAccounts
	serviceAccountList := &v1.ServiceAccountList{}
	err = testutil.List(k8sClient, serviceAccountList)
	require.NoError(t, err)
	require.Len(t, serviceAccountList.Items, 2)

	// Stork ServiceAccount
	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, storkServiceAccountName, storkSA.Name)
	require.Equal(t, cluster.Namespace, storkSA.Namespace)
	require.Len(t, storkSA.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkSA.OwnerReferences[0].Name)

	// Stork Scheduler ServiceAccount
	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, storkSchedServiceAccountName, storkSchedSA.Name)
	require.Equal(t, cluster.Namespace, storkSchedSA.Namespace)
	require.Len(t, storkSchedSA.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkSchedSA.OwnerReferences[0].Name)

	// ClusterRoles
	clusterRoleList := &rbacv1.ClusterRoleList{}
	err = testutil.List(k8sClient, clusterRoleList)
	require.NoError(t, err)
	require.Len(t, clusterRoleList.Items, 2)

	// Stork ClusterRole
	expectedStorkCR := testutil.GetExpectedClusterRole(t, "storkClusterRole.yaml")
	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedStorkCR.Name, storkCR.Name)
	require.Len(t, storkCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedStorkCR.Rules, storkCR.Rules)

	// Stork Scheduler ClusterRole
	expectedSchedCR := testutil.GetExpectedClusterRole(t, "storkSchedClusterRole.yaml")
	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)
	require.Equal(t, expectedSchedCR.Name, schedCR.Name)
	require.Len(t, schedCR.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedCR.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedSchedCR.Rules, schedCR.Rules)

	// ClusterRoleBindings
	crbList := &rbacv1.ClusterRoleBindingList{}
	err = testutil.List(k8sClient, crbList)
	require.NoError(t, err)
	require.Len(t, crbList.Items, 2)

	// Stork ClusterRoleBinding
	expectedStorkCRB := testutil.GetExpectedClusterRoleBinding(t, "storkClusterRoleBinding.yaml")
	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedStorkCRB.Name, storkCRB.Name)
	require.Len(t, storkCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedStorkCRB.Subjects, storkCRB.Subjects)
	require.Equal(t, expectedStorkCRB.RoleRef, storkCRB.RoleRef)

	// Stork Scheduler ClusterRoleBinding
	expectedSchedCRB := testutil.GetExpectedClusterRoleBinding(t, "storkSchedClusterRoleBinding.yaml")
	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)
	require.Equal(t, expectedSchedCRB.Name, schedCRB.Name)
	require.Len(t, schedCRB.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedCRB.OwnerReferences[0].Name)
	require.ElementsMatch(t, expectedSchedCRB.Subjects, schedCRB.Subjects)
	require.Equal(t, expectedSchedCRB.RoleRef, schedCRB.RoleRef)

	// Stork Service
	serviceList := &v1.ServiceList{}
	err = testutil.List(k8sClient, serviceList)
	require.NoError(t, err)
	require.Len(t, serviceList.Items, 1)

	expectedService := testutil.GetExpectedService(t, "storkService.yaml")
	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedService.Name, storkService.Name)
	require.Equal(t, expectedService.Namespace, storkService.Namespace)
	require.Len(t, storkService.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkService.OwnerReferences[0].Name)
	require.Equal(t, expectedService.Labels, storkService.Labels)
	require.Equal(t, expectedService.Spec, storkService.Spec)

	// Deployments
	deploymentList := &appsv1.DeploymentList{}
	err = testutil.List(k8sClient, deploymentList)
	require.NoError(t, err)
	require.Len(t, deploymentList.Items, 2)

	// Stork Deployment
	expectedStorkDeployment := testutil.GetExpectedDeployment(t, "storkDeployment.yaml")
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedStorkDeployment.Name, storkDeployment.Name)
	require.Equal(t, expectedStorkDeployment.Namespace, storkDeployment.Namespace)
	require.Len(t, storkDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedStorkDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	storkDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedStorkDeployment.Labels, storkDeployment.Labels)
	require.Equal(t, expectedStorkDeployment.Annotations, storkDeployment.Annotations)
	require.Equal(t, expectedStorkDeployment.Spec, storkDeployment.Spec)

	// Sched Scheduler Deployment
	expectedSchedDeployment := testutil.GetExpectedDeployment(t, "storkSchedDeployment.yaml")
	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, expectedSchedDeployment.Name, schedDeployment.Name)
	require.Equal(t, expectedSchedDeployment.Namespace, schedDeployment.Namespace)
	require.Len(t, schedDeployment.OwnerReferences, 1)
	require.Equal(t, cluster.Name, schedDeployment.OwnerReferences[0].Name)
	// Ignoring resource comparison as the parsing from string creates different objects
	expectedSchedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	schedDeployment.Spec.Template.Spec.Containers[0].Resources.Requests = nil
	require.Equal(t, expectedSchedDeployment.Labels, schedDeployment.Labels)
	require.Equal(t, expectedSchedDeployment.Spec, schedDeployment.Spec)

	// Stork Snapshot StorageClass
	storkStorageClass := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkStorageClass, storkSnapshotStorageClassName, "")
	require.NoError(t, err)
	require.Equal(t, storkSnapshotStorageClassName, storkStorageClass.Name)
	require.Len(t, storkStorageClass.OwnerReferences, 1)
	require.Equal(t, cluster.Name, storkStorageClass.OwnerReferences[0].Name)
	require.Equal(t, "stork-snapshot", storkStorageClass.Provisioner)
}

func TestStorkWithoutImage(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
			},
		},
	}

	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            testutil.FakeK8sClient(cluster),
		Driver:            driver,
		kubernetesVersion: k8sVersion,
		recorder:          recorder,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork. stork image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)

	cluster.Spec.Stork.Image = ""
	err = controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork. stork image cannot be empty",
			v1.EventTypeWarning, util.FailedComponentReason),
	)
}

func TestStorkImageChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:v1",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "osd/stork:v1", storkDeployment.Spec.Template.Spec.Containers[0].Image)

	// Change the stork image
	cluster.Spec.Stork.Image = "osd/stork:v2"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t, "osd/stork:v2", storkDeployment.Spec.Template.Spec.Containers[0].Image)
}

func TestStorkArgumentsChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// Check custom new arg is present
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.Containers[0].Command, 6)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--test-key=test-value",
	)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--verbose=true",
	)

	// Overwrite existing argument with new value
	cluster.Spec.Stork.Args["verbose"] = "false"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.Containers[0].Command, 6)
	require.Contains(t,
		storkDeployment.Spec.Template.Spec.Containers[0].Command,
		"--verbose=false",
	)
}

func TestStorkEnvVarsChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Env: []v1.EnvVar{
					{
						Name:  "FOO",
						Value: "foo",
					},
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driverEnvs := []v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}
	driver.EXPECT().GetStorkEnvList(cluster).
		Return(driverEnvs).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	// Check envs are passed to deployment
	expectedEnvs := append(driverEnvs, cluster.Spec.Stork.Env...)
	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)

	// Overwrite existing envs
	cluster.Spec.Stork.Env[0].Value = "bar"

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedEnvs[1].Value = "bar"
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)

	// Add new env vars
	newEnv := v1.EnvVar{Name: "BAZ", Value: "baz"}
	cluster.Spec.Stork.Env = append(cluster.Spec.Stork.Env, newEnv)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedEnvs = append(expectedEnvs, newEnv)
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, storkDeployment.Spec.Template.Spec.Containers[0].Env, expectedEnvs)
}

func TestStorkCPUChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity := resource.MustParse(defaultStorkCPU)
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for Stork deployment
	expectedCPU := "0.2"
	cluster.Annotations = map[string]string{annotationStorkCPU: expectedCPU}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestStorkSchedulerCPUChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity := resource.MustParse(defaultStorkCPU)
	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))

	// Change the CPU resource required for Stork scheduler deployment
	expectedCPU := "0.2"
	cluster.Annotations = map[string]string{annotationStorkSchedCPU: expectedCPU}

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	expectedCPUQuantity = resource.MustParse(expectedCPU)
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Zero(t, expectedCPUQuantity.Cmp(deployment.Spec.Template.Spec.Containers[0].Resources.Requests[v1.ResourceCPU]))
}

func TestStorkInvalidCPU(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationStorkCPU: "invalid-cpu",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()

	// Should not return error, instead raise an event
	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestStorkSchedulerInvalidCPU(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
			Annotations: map[string]string{
				annotationStorkSchedCPU: "invalid-cpu",
			},
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	recorder := record.NewFakeRecorder(10)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		recorder:          recorder,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	// Should not return error, instead raise an event
	err := controller.syncStork(cluster)
	require.NoError(t, err)
	require.Len(t, recorder.Events, 1)
	require.Contains(t, <-recorder.Events,
		fmt.Sprintf("%v %v Failed to setup Stork.", v1.EventTypeWarning, util.FailedComponentReason))
}

func TestStorkSchedulerRollbackImageChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-scheduler-amd64:v1.11.0",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)

	// Change the image of stork scheduler deployment
	deployment.Spec.Template.Spec.Containers[0].Image = "foo/bar:v1"
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		"gcr.io/google_containers/kube-scheduler-amd64:v1.11.0",
		deployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkSchedulerRollbackCommandChange(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
				Args: map[string]string{
					"test-key": "test-value",
				},
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	deployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	expectedCommand := deployment.Spec.Template.Spec.Containers[0].Command

	// Change the command of stork scheduler deployment
	deployment.Spec.Template.Spec.Containers[0].Command = append(
		deployment.Spec.Template.Spec.Containers[0].Command,
		"--new-arg=test",
	)
	err = k8sClient.Update(context.TODO(), deployment)
	require.NoError(t, err)

	err = controller.syncStork(cluster)
	require.NoError(t, err)

	err = testutil.Get(k8sClient, deployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.ElementsMatch(t, expectedCommand, deployment.Spec.Template.Spec.Containers[0].Command)
}

func TestStorkInstallWithCustomRepoRegistry(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	customRepo := "test-registry:1111/test-repo"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CustomImageRegistry: customRepo,
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRepo+"/kube-scheduler-amd64:v1.11.0",
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkInstallWithCustomRegistry(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	customRegistry := "test-registry:1111"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			CustomImageRegistry: customRegistry,
			ImagePullPolicy:     v1.PullIfNotPresent,
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/osd/stork:test",
		storkDeployment.Spec.Template.Spec.Containers[0].Image,
	)
	require.Equal(t,
		v1.PullIfNotPresent,
		storkDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Equal(t,
		customRegistry+"/gcr.io/google_containers/kube-scheduler-amd64:v1.11.0",
		schedDeployment.Spec.Template.Spec.Containers[0].Image,
	)
}

func TestStorkInstallWithImagePullSecret(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	imagePullSecret := "registry-secret"
	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			ImagePullSecret: &imagePullSecret,
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t,
		imagePullSecret,
		storkDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)
	require.Len(t, storkDeployment.Spec.Template.Spec.ImagePullSecrets, 1)
	require.Equal(t,
		imagePullSecret,
		schedDeployment.Spec.Template.Spec.ImagePullSecrets[0].Name,
	)
}

func TestDisableStork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.NoError(t, err)

	// Disable Stork
	cluster.Spec.Stork.Enabled = false
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService = &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestRemoveStork(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("pxd", nil).AnyTimes()
	driver.EXPECT().GetStorkEnvList(cluster).
		Return([]v1.EnvVar{{Name: "PX_NAMESPACE", Value: cluster.Namespace}}).
		AnyTimes()

	err := controller.syncStork(cluster)

	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.NoError(t, err)

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.NoError(t, err)

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.NoError(t, err)

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.NoError(t, err)

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.NoError(t, err)

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.NoError(t, err)

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.NoError(t, err)

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.NoError(t, err)

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.NoError(t, err)

	// Remove Stork config
	cluster.Spec.Stork = nil
	err = controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM = &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA = &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR = &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB = &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService = &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment = &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC = &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}

func TestStorkDriverNotImplemented(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()

	cluster := &corev1alpha1.StorageCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "px-cluster",
			Namespace: "kube-test",
		},
		Spec: corev1alpha1.StorageClusterSpec{
			Stork: &corev1alpha1.StorkSpec{
				Enabled: true,
				Image:   "osd/stork:test",
			},
		},
	}

	k8s.Instance().SetBaseClient(fakek8sclient.NewSimpleClientset())
	k8sVersion, _ := version.NewVersion("1.11.0")
	driver := testutil.MockDriver(mockCtrl)
	k8sClient := testutil.FakeK8sClient(cluster)
	controller := Controller{
		client:            k8sClient,
		Driver:            driver,
		kubernetesVersion: k8sVersion,
	}

	driver.EXPECT().GetStorkDriverName().Return("", fmt.Errorf("not supported"))
	driver.EXPECT().String().Return("mock")

	err := controller.syncStork(cluster)
	require.NoError(t, err)

	storkCM := &v1.ConfigMap{}
	err = testutil.Get(k8sClient, storkCM, storkConfigMapName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSA, storkServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSchedSA := &v1.ServiceAccount{}
	err = testutil.Get(k8sClient, storkSchedSA, storkSchedServiceAccountName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, storkCR, storkClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	schedCR := &rbacv1.ClusterRole{}
	err = testutil.Get(k8sClient, schedCR, storkSchedClusterRoleName, "")
	require.True(t, errors.IsNotFound(err))

	storkCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, storkCRB, storkClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	schedCRB := &rbacv1.ClusterRoleBinding{}
	err = testutil.Get(k8sClient, schedCRB, storkSchedClusterRoleBindingName, "")
	require.True(t, errors.IsNotFound(err))

	storkService := &v1.Service{}
	err = testutil.Get(k8sClient, storkService, storkServiceName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, storkDeployment, storkDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	schedDeployment := &appsv1.Deployment{}
	err = testutil.Get(k8sClient, schedDeployment, storkSchedDeploymentName, cluster.Namespace)
	require.True(t, errors.IsNotFound(err))

	storkSC := &storagev1.StorageClass{}
	err = testutil.Get(k8sClient, storkSC, storkSnapshotStorageClassName, "")
	require.True(t, errors.IsNotFound(err))
}
