package portworx

import (
	"fmt"

	storage "github.com/libopenstorage/operator/drivers/storage"
	corev1alpha1 "github.com/libopenstorage/operator/pkg/apis/core/v1alpha1"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
)

const (
	// driverName is the name of the portworx storage driver implementation
	driverName             = "portworx"
	labelKeyName           = "name"
	defaultStartPort       = 9001
	defaultSecretsProvider = "k8s"
	defaultNodeWiperImage  = "adityadani/px-node-wiper"
	defaultNodeWiperTag    = "latest"
)

type portworx struct{}

func (p *portworx) String() string {
	return driverName
}

func (p *portworx) Init(_ interface{}) error {
	return nil
}

func (p *portworx) GetSelectorLabels() map[string]string {
	return map[string]string{
		labelKeyName: driverName,
	}
}

func (p *portworx) SetDefaultsOnStorageCluster(toUpdate *corev1alpha1.StorageCluster) {
	startPort := uint32(defaultStartPort)
	if toUpdate.Spec.Kvdb == nil || len(toUpdate.Spec.Kvdb.Endpoints) == 0 {
		toUpdate.Spec.Kvdb = &corev1alpha1.KvdbSpec{
			Internal: true,
		}
	}
	if toUpdate.Spec.SecretsProvider == nil {
		toUpdate.Spec.SecretsProvider = stringPtr(defaultSecretsProvider)
	}
	if toUpdate.Spec.StartPort == nil {
		toUpdate.Spec.StartPort = &startPort
	}
	if toUpdate.Spec.Placement == nil || toUpdate.Spec.Placement.NodeAffinity == nil {
		t, err := newTemplate(toUpdate)
		if err != nil {
			return
		}
		toUpdate.Spec.Placement = &corev1alpha1.PlacementSpec{
			NodeAffinity: &v1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
					NodeSelectorTerms: []v1.NodeSelectorTerm{
						{
							MatchExpressions: t.getSelectorRequirements(),
						},
					},
				},
			},
		}
	}
}

func (p *portworx) DeleteStorage(storageCluster *corev1alpha1.StorageCluster) (*corev1alpha1.StorageClusterDeleteStatus, error) {
	u := NewUninstaller(storageCluster)
	completed, inProgress, total, err := u.GetNodeWiperStatus()
	if err != nil && errors.IsNotFound(err) {
		// Run the node wiper
		removeData := false
		if storageCluster.Spec.DeleteStrategy.Type == corev1alpha1.UninstallAndWipeStorageClusterStrategyType {
			removeData = true
		}
		// TODO: Add capability to change the node wiper image
		if err := u.RunNodeWiper(defaultNodeWiperImage, defaultNodeWiperTag, removeData); err != nil {
			status := &corev1alpha1.StorageClusterDeleteStatus{
				Status:  corev1alpha1.FailedStorageClusterDeleteStatusType,
				Message: "Failed to run node wiper: " + err.Error(),
			}
			return status, nil
		}
		status := &corev1alpha1.StorageClusterDeleteStatus{
			Status:  corev1alpha1.InProgressStorageClusterDeleteStatusType,
			Message: "Started node wiper daemonset",
		}
		return status, nil
	} else if err != nil {
		// We could not get the node wiper status and it does exist
		// retry?
		return nil, err
	} // else err == nil

	if completed != 0 && total != 0 && completed == total {
		// all the nodes are wiped
		status := &corev1alpha1.StorageClusterDeleteStatus{
			Status: corev1alpha1.CompletedStorageClusterDeleteStatusType,
		}
		if err := u.DeleteNodeWiper(); err != nil {
			logrus.Errorf("Failed to delete node wiper daemonset: %v", err)
		}
		if err := u.WipeMetadata(); err != nil {
			logrus.Errorf("Failed to delete portworx metadata: %v", err)
		}
		return status, nil
	}

	status := &corev1alpha1.StorageClusterDeleteStatus{
		Status:  corev1alpha1.InProgressStorageClusterDeleteStatusType,
		Message: fmt.Sprintf("Wipe operation still in progress: Completed [%v] In Progress [%v] Total [%v]", completed, inProgress, total),
	}
	return status, nil
}

func init() {
	if err := storage.Register(driverName, &portworx{}); err != nil {
		logrus.Panicf("Error registering portworx storage driver: %v", err)
	}
}
