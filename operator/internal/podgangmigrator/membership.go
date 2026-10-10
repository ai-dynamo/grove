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

package podgangmigrator

// PodGangMembershipAnnotator is an optional capability a scheduler.Backend may satisfy to report the
// scheduler-specific annotations that express PodGang membership. The migrator applies these
// annotations to an existing Pod so it joins the epoch-based PodGroup during migration without being
// recreated.
//
// The normal Pod-creation path stamps this same membership metadata via Backend.PreparePod (for
// example Volcano's scheduling.k8s.io/group-name and scheduling.volcano.sh/group-name annotations).
// Migration does not recreate Pods, so it refreshes the annotations in place instead. Backends that
// carry no PodGang membership annotations on the Pod (for example the plain kube backend) do not
// implement this interface and are skipped.
type PodGangMembershipAnnotator interface {
	// PodGangMembershipAnnotations returns the scheduler-specific annotations that express membership
	// of the PodGang named newPodGangName.
	PodGangMembershipAnnotations(newPodGangName string) map[string]string
}
