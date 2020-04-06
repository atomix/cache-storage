// Copyright 2019-present Open Networking Foundation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"context"
	"errors"
	"fmt"
	api "github.com/atomix/api/proto/atomix/controller"
	"github.com/atomix/kubernetes-controller/pkg/apis/cloud/v1beta2"
	"github.com/atomix/kubernetes-controller/pkg/controller/v1beta2/storage"
	"github.com/atomix/kubernetes-controller/pkg/controller/v1beta2/util/k8s"
	"github.com/atomix/local-replica/pkg/apis/storage/v1beta1"
	"github.com/gogo/protobuf/jsonpb"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"strconv"
)

const (
	configPath         = "/etc/atomix"
	clusterConfigFile  = "cluster.json"
	protocolConfigFile = "protocol.json"
	dataPath           = "/var/lib/atomix"
)

const port = 5678

var log = logf.Log.WithName("controller_test")

// Add creates a new Partition ManagementGroup and adds it to the Manager. The Manager will set fields on the ManagementGroup
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	reconciler := &Reconciler{
		client: mgr.GetClient(),
		scheme: mgr.GetScheme(),
	}
	gvk := schema.GroupVersionKind{
		Group:   v1beta1.CacheStorageGroup,
		Version: v1beta1.CacheStorageVersion,
		Kind:    v1beta1.CacheStorageKind,
	}
	return storage.AddClusterReconciler(mgr, reconciler, gvk)
}

var _ reconcile.Reconciler = &Reconciler{}

// Reconciler reconciles a Cluster object
type Reconciler struct {
	client client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Cluster object and makes changes based on the state read
// and what is in the Cluster.Spec
func (r *Reconciler) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	cluster := &v1beta2.Cluster{}
	err := r.client.Get(context.TODO(), request.NamespacedName, cluster)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{Requeue: true}, err
	}

	storage := &v1beta1.CacheStorage{}
	name := types.NamespacedName{
		Namespace: cluster.Spec.Storage.Namespace,
		Name:      cluster.Spec.Storage.Name,
	}
	err = r.client.Get(context.TODO(), name, storage)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{Requeue: true}, err
	}

	err = r.reconcileConfigMap(cluster, storage)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.reconcileDeployment(cluster, storage)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.reconcileService(cluster, storage)
	if err != nil {
		return reconcile.Result{}, err
	}

	err = r.reconcileStatus(cluster, storage)
	if err != nil {
		return reconcile.Result{}, err
	}
	return reconcile.Result{}, nil
}

func (r *Reconciler) reconcileConfigMap(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	cm := &corev1.ConfigMap{}
	name := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}
	err := r.client.Get(context.TODO(), name, cm)
	if err != nil && k8serrors.IsNotFound(err) {
		err = r.addConfigMap(cluster, storage)
	}
	return err
}

func (r *Reconciler) addConfigMap(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	log.Info("Creating ConfigMap", "Name", cluster.Name, "Namespace", cluster.Namespace)

	config, err := newClusterConfig(cluster)
	if err != nil {
		return err
	}

	marshaller := jsonpb.Marshaler{}
	data, err := marshaller.MarshalToString(config)
	if err != nil {
		return err
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		},
		Data: map[string]string{
			clusterConfigFile: data,
		},
	}
	if err := controllerutil.SetControllerReference(cluster, cm, r.scheme); err != nil {
		return err
	}
	return r.client.Create(context.TODO(), cm)
}

func (r *Reconciler) reconcileDeployment(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	dep := &appsv1.Deployment{}
	name := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}
	err := r.client.Get(context.TODO(), name, dep)
	if err != nil && k8serrors.IsNotFound(err) {
		err = r.addDeployment(cluster, storage)
	}
	return err
}

func (r *Reconciler) addDeployment(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	log.Info("Creating Deployment", "Name", cluster.Name, "Namespace", cluster.Namespace)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		},
	}
	if err := controllerutil.SetControllerReference(cluster, dep, r.scheme); err != nil {
		return err
	}
	return r.client.Create(context.TODO(), dep)
}

func (r *Reconciler) reconcileService(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	service := &corev1.Service{}
	name := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}
	err := r.client.Get(context.TODO(), name, service)
	if err != nil && k8serrors.IsNotFound(err) {
		err = r.addService(cluster, storage)
	}
	return err
}

func (r *Reconciler) addService(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	log.Info("Creating service", "Name", cluster.Name, "Namespace", cluster.Namespace)
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: cluster.Namespace,
			Name:      cluster.Name,
		},
	}
	if err := controllerutil.SetControllerReference(cluster, service, r.scheme); err != nil {
		return err
	}
	return r.client.Create(context.TODO(), service)
}

func (r *Reconciler) reconcileStatus(cluster *v1beta2.Cluster, storage *v1beta1.CacheStorage) error {
	dep := &appsv1.Deployment{}
	name := types.NamespacedName{
		Namespace: cluster.Namespace,
		Name:      cluster.Name,
	}
	err := r.client.Get(context.TODO(), name, dep)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if cluster.Status.ReadyPartitions < cluster.Spec.Partitions &&
		dep.Status.ReadyReplicas == dep.Status.Replicas {
		clusterID, err := k8s.GetClusterIDFromClusterAnnotations(cluster)
		if err != nil {
			return err
		}
		for partitionID := (cluster.Spec.Partitions * (clusterID - 1)) + 1; partitionID <= cluster.Spec.Partitions*clusterID; partitionID++ {
			partition := &v1beta2.Partition{}
			err := r.client.Get(context.TODO(), k8s.GetPartitionNamespacedName(cluster, partitionID), partition)
			if err != nil && !k8serrors.IsNotFound(err) {
				return err
			}
			if !partition.Status.Ready {
				partition.Status.Ready = true
				log.Info("Updating Partition status", "Name", partition.Name, "Namespace", partition.Namespace, "Ready", partition.Status.Ready)
				err = r.client.Status().Update(context.TODO(), partition)
				if err != nil {
					return err
				}
			}
		}

		// If we've made it this far, all partitions are ready. Update the cluster status
		cluster.Status.ReadyPartitions = cluster.Spec.Partitions
		log.Info("Updating Cluster status", "Name", cluster.Name, "Namespace", cluster.Namespace, "ReadyPartitions", cluster.Status.ReadyPartitions)
		return r.client.Status().Update(context.TODO(), cluster)
	}
	return nil
}

// newNodeConfigString creates a node configuration string for the given cluster
func newClusterConfig(cluster *v1beta2.Cluster) (*api.ClusterConfig, error) {
	database := cluster.Annotations["cloud.atomix.io/database"]

	clusterIDstr, ok := cluster.Annotations["cloud.atomix.io/cluster"]
	if !ok {
		return nil, errors.New("missing cluster annotation")
	}

	id, err := strconv.ParseInt(clusterIDstr, 0, 32)
	if err != nil {
		return nil, err
	}
	clusterID := int32(id)

	members := []*api.MemberConfig{
		{
			ID:           cluster.Name,
			Host:         fmt.Sprintf("%s.%s.svc.cluster.local", cluster.Namespace, cluster.Name),
			ProtocolPort: port,
			APIPort:      port,
		},
	}

	partitions := make([]*api.PartitionId, 0, cluster.Spec.Partitions)
	for partitionID := (cluster.Spec.Partitions * (clusterID - 1)) + 1; partitionID <= cluster.Spec.Partitions*clusterID; partitionID++ {
		partition := &api.PartitionId{
			Partition: partitionID,
			Cluster: &api.ClusterId{
				ID: int32(clusterID),
				DatabaseID: &api.DatabaseId{
					Name:      database,
					Namespace: cluster.Namespace,
				},
			},
		}
		partitions = append(partitions, partition)
	}

	return &api.ClusterConfig{
		Members:    members,
		Partitions: partitions,
	}, nil
}
