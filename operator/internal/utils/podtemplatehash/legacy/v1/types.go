// Copyright 2015 The Kubernetes Authors.
// Copyright 2026 The Grove Authors.
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

// Package v1 preserves the Kubernetes 1.34 struct layouts that changed in 1.35.
// Legacy hashes include Go type names, field names, field order, and zero values.
// Keep the package name and these layouts unchanged. These types are hash inputs,
// not API objects; unchanged nested types use the Kubernetes 1.35 definitions.
package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodTemplateSpec is the legacy hash input.
type PodTemplateSpec struct {
	metav1.ObjectMeta
	Spec PodSpec
}

// PodSpec predates WorkloadRef.
type PodSpec struct {
	Volumes                       []Volume
	InitContainers                []corev1.Container
	Containers                    []corev1.Container
	EphemeralContainers           []corev1.EphemeralContainer
	RestartPolicy                 corev1.RestartPolicy
	TerminationGracePeriodSeconds *int64
	ActiveDeadlineSeconds         *int64
	DNSPolicy                     corev1.DNSPolicy
	NodeSelector                  map[string]string
	ServiceAccountName            string
	DeprecatedServiceAccount      string
	AutomountServiceAccountToken  *bool
	NodeName                      string
	HostNetwork                   bool
	HostPID                       bool
	HostIPC                       bool
	ShareProcessNamespace         *bool
	SecurityContext               *corev1.PodSecurityContext
	ImagePullSecrets              []corev1.LocalObjectReference
	Hostname                      string
	Subdomain                     string
	Affinity                      *corev1.Affinity
	SchedulerName                 string
	Tolerations                   []corev1.Toleration
	HostAliases                   []corev1.HostAlias
	PriorityClassName             string
	Priority                      *int32
	DNSConfig                     *corev1.PodDNSConfig
	ReadinessGates                []corev1.PodReadinessGate
	RuntimeClassName              *string
	EnableServiceLinks            *bool
	PreemptionPolicy              *corev1.PreemptionPolicy
	Overhead                      corev1.ResourceList
	TopologySpreadConstraints     []corev1.TopologySpreadConstraint
	SetHostnameAsFQDN             *bool
	OS                            *corev1.PodOS
	HostUsers                     *bool
	SchedulingGates               []corev1.PodSchedulingGate
	ResourceClaims                []corev1.PodResourceClaim
	Resources                     *corev1.ResourceRequirements
	HostnameOverride              *string
}

// Volume preserves the enclosing type of VolumeSource.
type Volume struct {
	Name string
	VolumeSource
}

// VolumeSource preserves the enclosing type of ProjectedVolumeSource.
type VolumeSource struct {
	HostPath              *corev1.HostPathVolumeSource
	EmptyDir              *corev1.EmptyDirVolumeSource
	GCEPersistentDisk     *corev1.GCEPersistentDiskVolumeSource
	AWSElasticBlockStore  *corev1.AWSElasticBlockStoreVolumeSource
	GitRepo               *corev1.GitRepoVolumeSource
	Secret                *corev1.SecretVolumeSource
	NFS                   *corev1.NFSVolumeSource
	ISCSI                 *corev1.ISCSIVolumeSource
	Glusterfs             *corev1.GlusterfsVolumeSource
	PersistentVolumeClaim *corev1.PersistentVolumeClaimVolumeSource
	RBD                   *corev1.RBDVolumeSource
	FlexVolume            *corev1.FlexVolumeSource
	Cinder                *corev1.CinderVolumeSource
	CephFS                *corev1.CephFSVolumeSource
	Flocker               *corev1.FlockerVolumeSource
	DownwardAPI           *corev1.DownwardAPIVolumeSource
	FC                    *corev1.FCVolumeSource
	AzureFile             *corev1.AzureFileVolumeSource
	ConfigMap             *corev1.ConfigMapVolumeSource
	VsphereVolume         *corev1.VsphereVirtualDiskVolumeSource
	Quobyte               *corev1.QuobyteVolumeSource
	AzureDisk             *corev1.AzureDiskVolumeSource
	PhotonPersistentDisk  *corev1.PhotonPersistentDiskVolumeSource
	Projected             *ProjectedVolumeSource
	PortworxVolume        *corev1.PortworxVolumeSource
	ScaleIO               *corev1.ScaleIOVolumeSource
	StorageOS             *corev1.StorageOSVolumeSource
	CSI                   *corev1.CSIVolumeSource
	Ephemeral             *corev1.EphemeralVolumeSource
	Image                 *corev1.ImageVolumeSource
}

// ProjectedVolumeSource preserves the enclosing type of VolumeProjection.
type ProjectedVolumeSource struct {
	Sources     []VolumeProjection
	DefaultMode *int32
}

// VolumeProjection preserves the enclosing type of PodCertificateProjection.
type VolumeProjection struct {
	Secret              *corev1.SecretProjection
	DownwardAPI         *corev1.DownwardAPIProjection
	ConfigMap           *corev1.ConfigMapProjection
	ServiceAccountToken *corev1.ServiceAccountTokenProjection
	ClusterTrustBundle  *corev1.ClusterTrustBundleProjection
	PodCertificate      *PodCertificateProjection
}

// PodCertificateProjection predates UserAnnotations.
type PodCertificateProjection struct {
	SignerName           string
	KeyType              string
	MaxExpirationSeconds *int32
	CredentialBundlePath string
	KeyPath              string
	CertificateChainPath string
}
