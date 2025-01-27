// Copyright 2019 The Kubernetes Authors.
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

package mcmhub

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	chnv1alpha1 "github.com/IBM/multicloud-operators-channel/pkg/apis/app/v1alpha1"
	dplv1alpha1 "github.com/IBM/multicloud-operators-deployable/pkg/apis/app/v1alpha1"
	dplutils "github.com/IBM/multicloud-operators-deployable/pkg/utils"
	plrv1alpha1 "github.com/IBM/multicloud-operators-placementrule/pkg/apis/app/v1alpha1"
	appv1alpha1 "github.com/IBM/multicloud-operators-subscription/pkg/apis/app/v1alpha1"
	subutil "github.com/IBM/multicloud-operators-subscription/pkg/utils"
)

// doMCMHubReconcile process Subscription on hub - distribute it via deployable
func (r *ReconcileSubscription) doMCMHubReconcile(sub *appv1alpha1.Subscription) error {
	if r.UpdateDeployablesAnnotation(sub) {
		return nil
	}

	if sub.Spec.Placement == nil {
		err := r.stopDeploySubscription(sub)
		return err
	}

	dpl, err := r.prepareDeployableForSubscription(sub, nil)

	if err != nil {
		return err
	}

	// if the subscription has the rollingupdate-target annotation, create a new deploayble as the target deployable of the subscription deployable
	targetDpl, err := r.createTargetDplForRollingUpdate(sub)

	if err != nil {
		return err
	}

	if targetDpl != nil {
		dplAnno := dpl.GetAnnotations()

		if dplAnno == nil {
			dplAnno = make(map[string]string)
		}

		dplAnno[appv1alpha1.AnnotationRollingUpdateTarget] = targetDpl.GetName()

		dpl.SetAnnotations(dplAnno)
	}

	found := &dplv1alpha1.Deployable{}
	dplkey := types.NamespacedName{Name: dpl.Name, Namespace: dpl.Namespace}
	err = r.Get(context.TODO(), dplkey, found)

	if err != nil && errors.IsNotFound(err) {
		klog.V(5).Info("Creating Deployable - ", "namespace: ", dpl.Namespace, ", name: ", dpl.Name)
		err = r.Create(context.TODO(), dpl)

		//record events
		addtionalMsg := "Depolyable " + dplkey.String() + " created in the subscription namespace for deploying the subscription to managed clusters"
		r.eventRecorder.RecordEvent(sub, "Deploy", addtionalMsg, err)

		return err
	} else if err != nil {
		return err
	}

	org := &unstructured.Unstructured{}
	err = json.Unmarshal(dpl.Spec.Template.Raw, org)

	if err != nil {
		klog.V(5).Info("Error in unmarshall, err:", err, " |template: ", string(dpl.Spec.Template.Raw))
		return err
	}

	fnd := &unstructured.Unstructured{}
	err = json.Unmarshal(found.Spec.Template.Raw, fnd)

	if err != nil {
		klog.V(5).Info("Error in unmarshall, err:", err, " |template: ", string(found.Spec.Template.Raw))
		return err
	}

	if !reflect.DeepEqual(org, fnd) {
		klog.V(5).Info("Updating Deployable spec:\n", string(dpl.Spec.Template.Raw), "\nfound:\n", string(found.Spec.Template.Raw))

		dpl.Spec.DeepCopyInto(&found.Spec)
		// may need to check owner ID and backoff it if is not owned by this subscription

		foundanno := found.GetAnnotations()
		if foundanno == nil {
			foundanno = make(map[string]string)
		}

		foundanno[dplv1alpha1.AnnotationIsGenerated] = "true"
		foundanno[dplv1alpha1.AnnotationLocal] = "false"
		found.SetAnnotations(foundanno)

		klog.V(5).Info("Updating Deployable - ", "namespace: ", dpl.Namespace, " ,name: ", dpl.Name)

		err = r.Update(context.TODO(), found)

		//record events
		addtionalMsg := "Depolyable " + dplkey.String() + " updated in the subscription namespace for deploying the subscription to managed clusters"
		r.eventRecorder.RecordEvent(sub, "Deploy", addtionalMsg, err)

		if err != nil {
			return err
		}
	} else {
		err = r.updateSubscriptionStatus(sub, found)
	}

	return err
}

//GetChannelNamespaceType get the channel namespace and channel type by the given subscription
func (r *ReconcileSubscription) GetChannelNamespaceType(s *appv1alpha1.Subscription) (string, string) {
	chNameSpace := ""
	chName := ""
	chType := ""

	if s.Spec.Channel != "" {
		strs := strings.Split(s.Spec.Channel, "/")
		if len(strs) == 2 {
			chNameSpace = strs[0]
			chName = strs[1]
		} else {
			chNameSpace = s.Namespace
		}
	}

	chkey := types.NamespacedName{Name: chName, Namespace: chNameSpace}
	chobj := &chnv1alpha1.Channel{}
	err := r.Get(context.TODO(), chkey, chobj)

	if err == nil {
		chType = string(chobj.Spec.Type)
	}

	return chNameSpace, chType
}

// GetChannelGeneration get the channel generation
func (r *ReconcileSubscription) GetChannelGeneration(s *appv1alpha1.Subscription) (string, error) {
	chNameSpace := ""
	chName := ""

	if s.Spec.Channel != "" {
		strs := strings.Split(s.Spec.Channel, "/")
		if len(strs) == 2 {
			chNameSpace = strs[0]
			chName = strs[1]
		} else {
			chNameSpace = s.Namespace
		}
	}

	chkey := types.NamespacedName{Name: chName, Namespace: chNameSpace}
	chobj := &chnv1alpha1.Channel{}
	err := r.Get(context.TODO(), chkey, chobj)

	if err != nil {
		return "", err
	}

	return strconv.FormatInt(chobj.Generation, 10), nil
}

// UpdateDeployablesAnnotation set all deployables subscribed by the subscription to the app.ibm.com/deployables annotation
func (r *ReconcileSubscription) UpdateDeployablesAnnotation(sub *appv1alpha1.Subscription) bool {
	orgdplmap := make(map[string]bool)
	organno := sub.GetAnnotations()

	if organno != nil {
		dpls := organno[appv1alpha1.AnnotationDeployables]
		if dpls != "" {
			dplkeys := strings.Split(dpls, ",")
			for _, dplkey := range dplkeys {
				orgdplmap[dplkey] = true
			}
		}
	}

	allDpls := r.getSubscriptionDeployables(sub)

	// changes in order of deployables does not mean changes in deployables
	updated := false

	for k := range allDpls {
		if _, ok := orgdplmap[k]; !ok {
			updated = true
			break
		}

		delete(orgdplmap, k)
	}

	if !updated && len(orgdplmap) > 0 {
		updated = true
	}

	if updated {
		dplstr := ""
		for dplkey := range allDpls {
			if dplstr != "" {
				dplstr += ","
			}

			dplstr += dplkey
		}

		klog.Info("subscription updated for ", sub.Namespace, "/", sub.Name, " new deployables:", dplstr)

		subanno := sub.GetAnnotations()
		if subanno == nil {
			subanno = make(map[string]string)
		}

		subanno[appv1alpha1.AnnotationDeployables] = dplstr
		sub.SetAnnotations(subanno)

		err := r.Update(context.TODO(), sub)
		if err != nil {
			klog.Infof("Updating Subscription annotation app.ibm.com/Deployables failed. subscription: %#v, error: %#v", sub, err)
		}
	} else {
		klog.V(5).Info("subscription update, same spec, Skipping ", sub.Namespace, "/", sub.Name)
	}

	return updated
}

// stopDeploySubscription stop deploying the subscription if there is no placement for the subscription.
// As a result, the subscription deployable is removed and the subscription status is updated accordingly
func (r *ReconcileSubscription) stopDeploySubscription(sub *appv1alpha1.Subscription) error {
	hubdpl := &dplv1alpha1.Deployable{}
	err := r.Get(context.TODO(), types.NamespacedName{Name: sub.Name + "-deployable", Namespace: sub.Namespace}, hubdpl)

	if err == nil {
		// no longer hub, check owner-reference and delete if it is generated.
		owners := hubdpl.GetOwnerReferences()
		for _, owner := range owners {
			if owner.UID == sub.UID {
				err = r.Delete(context.TODO(), hubdpl)
				if err != nil {
					klog.V(5).Infof("Error in deleting sbuscription target deploayble: %#v, err: %#v ", hubdpl, err)
					return err
				}
			}
		}
	}

	// delete target deployable if exists. This only happens when the subscription placement becomes empty
	hubTargetDpl := &dplv1alpha1.Deployable{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: sub.Name + "-target-deployable", Namespace: sub.Namespace}, hubTargetDpl)

	if err == nil {
		// no longer hub, check owner-reference and delete if it is generated.
		owners := hubTargetDpl.GetOwnerReferences()
		for _, owner := range owners {
			if owner.UID == sub.UID {
				err = r.Delete(context.TODO(), hubTargetDpl)
				if err != nil {
					klog.Infof("Error in deleting sbuscription target deploayble: %#v, err: %v", hubTargetDpl, err)
					return err
				}
			}
		}
	}

	savest := sub.Status.DeepCopy()

	if sub.Status.Statuses != nil {
		for k := range sub.Status.Statuses {
			delete(sub.Status.Statuses, k)
		}
	}

	if sub.Status.Phase == appv1alpha1.SubscriptionPropagated {
		sub.Status.Phase = ""
		sub.Status.Message = ""
		sub.Status.Reason = ""
	}

	if !reflect.DeepEqual(savest, sub.Status) {
		sub.Status.LastUpdateTime = metav1.Now()
		err = r.Status().Update(context.TODO(), sub)

		if err != nil {
			klog.Info("Error in updating subscription obj: ", sub, err)
			return err
		}
	}

	return nil
}

func (r *ReconcileSubscription) prepareDeployableForSubscription(sub, rootSub *appv1alpha1.Subscription) (*dplv1alpha1.Deployable, error) {
	// Fetch the Subscription instance
	subep := sub.DeepCopy()
	b := true
	subep.Spec.Placement = &plrv1alpha1.Placement{Local: &b}
	subep.Spec.Overrides = nil
	subep.ResourceVersion = ""
	subep.UID = ""

	subep.CreationTimestamp = metav1.Time{}
	subep.Generation = 1
	subep.SelfLink = ""

	subepanno := make(map[string]string)

	if rootSub == nil {
		subep.Name = sub.GetName()
		subepanno[dplv1alpha1.AnnotationSubscription] = subep.Namespace + "/" + subep.Name
	} else {
		subep.Name = rootSub.GetName()
		subepanno[dplv1alpha1.AnnotationSubscription] = rootSub.Namespace + "/" + rootSub.Name
	}
	// set channel generation as annotation
	if subep.Spec.Channel != "" {
		chng, err := r.GetChannelGeneration(subep)
		if err == nil {
			subepanno[appv1alpha1.AnnotationChannelGeneration] = chng
		}
	}

	subep.SetAnnotations(subepanno)
	subep.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   appv1alpha1.SchemeGroupVersion.Group,
		Version: appv1alpha1.SchemeGroupVersion.Version,
		Kind:    "Subscription",
	})
	(&appv1alpha1.SubscriptionStatus{}).DeepCopyInto(&subep.Status)

	rawep, err := json.Marshal(subep)
	if err != nil {
		klog.Info("Error in mashalling subscription ", subep, err)
		return nil, err
	}

	dpl := &dplv1alpha1.Deployable{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Deployable",
			APIVersion: "app.ibm.com/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      sub.Name + "-deployable",
			Namespace: sub.Namespace,
			Annotations: map[string]string{
				dplv1alpha1.AnnotationLocal:       "false",
				dplv1alpha1.AnnotationIsGenerated: "true",
			},
		},
		Spec: dplv1alpha1.DeployableSpec{
			Template: &runtime.RawExtension{
				Raw: rawep,
			},
			Placement: sub.Spec.Placement,
		},
	}
	if err = controllerutil.SetControllerReference(sub, dpl, r.scheme); err != nil {
		return nil, err
	}

	// apply "/" override to template, and carry other overrides to deployable.
	for _, ov := range sub.Spec.Overrides {
		if ov.ClusterName == "/" {
			tplobj := &unstructured.Unstructured{}
			err = json.Unmarshal(dpl.Spec.Template.Raw, tplobj)

			if err != nil {
				klog.Info("Error in unmarshall, err:", err, " |template: ", string(dpl.Spec.Template.Raw))
				return nil, err
			}

			tplobj, err = dplutils.OverrideTemplate(tplobj, ov.ClusterOverrides)
			if err != nil {
				klog.Info("Error in overriding obj ", tplobj, err)
				return nil, err
			}

			dpl.Spec.Template.Raw, err = json.Marshal(tplobj)
			if err != nil {
				klog.Info("Error in mashalling obj ", tplobj, err)
				return nil, err
			}
		} else {
			dplov := dplv1alpha1.Overrides{}
			ov.DeepCopyInto(&dplov)
			dpl.Spec.Overrides = append(dpl.Spec.Overrides, dplov)
		}
	}

	return dpl, nil
}

func (r *ReconcileSubscription) updateSubscriptionStatus(sub *appv1alpha1.Subscription, found *dplv1alpha1.Deployable) error {
	newsubstatus := appv1alpha1.SubscriptionStatus{}

	newsubstatus.Phase = appv1alpha1.SubscriptionPropagated
	newsubstatus.Message = ""
	newsubstatus.Reason = ""

	if found.Status.Phase == dplv1alpha1.DeployableFailed {
		newsubstatus.Statuses = nil
	} else {
		newsubstatus.Statuses = make(map[string]*appv1alpha1.SubscriptionPerClusterStatus)

		for k, v := range found.Status.PropagatedStatus {
			clusterSubStatus := &appv1alpha1.SubscriptionPerClusterStatus{}
			if v.Phase == dplv1alpha1.DeployableDeployed {
				mcsubstatus := &appv1alpha1.SubscriptionStatus{}
				if v.ResourceStatus != nil {
					err := json.Unmarshal(v.ResourceStatus.Raw, mcsubstatus)
					if err != nil {
						klog.Info("Failed to unmashall status from clusters")
						return err
					}
				}
				clusterSubStatus = mcsubstatus.Statuses["/"]
			}
			newsubstatus.Statuses[k] = clusterSubStatus
		}
	}

	newsubstatus.LastUpdateTime = sub.Status.LastUpdateTime
	klog.V(5).Info("Check status for ", sub.Namespace, "/", sub.Name, " with ", newsubstatus)

	if !reflect.DeepEqual(newsubstatus, sub.Status) {
		newsubstatus.DeepCopyInto(&sub.Status)
		sub.Status.LastUpdateTime = metav1.Now()

		klog.V(5).Info("Do Updating status for ", sub.Namespace, "/", sub.Name, " with ", sub.Status)
		err := r.Status().Update(context.TODO(), sub)

		if err != nil {
			klog.Info("Failed to update hub subscription status. error: ", err, "\n sub: ", sub)
		}
	}

	return nil
}

func (r *ReconcileSubscription) getSubscriptionDeployables(sub *appv1alpha1.Subscription) map[string]*dplv1alpha1.Deployable {
	allDpls := make(map[string]*dplv1alpha1.Deployable)

	dplList := &dplv1alpha1.DeployableList{}

	chNameSpace, _ := r.GetChannelNamespaceType(sub)

	dplListOptions := &client.ListOptions{Namespace: chNameSpace}

	if sub.Spec.PackageFilter != nil && sub.Spec.PackageFilter.LabelSelector != nil {
		clSelector, err := dplutils.ConvertLabels(sub.Spec.PackageFilter.LabelSelector)
		if err != nil {
			klog.Error("Failed to set label selector of subscrption:", sub.Spec.PackageFilter.LabelSelector, " err: ", err)
			return nil
		}

		dplListOptions.LabelSelector = clSelector
	}

	err := r.Client.List(context.TODO(), dplList, dplListOptions)

	if err != nil {
		klog.Error("Failed to list objects from sbuscription namespace ", sub.Namespace, " err: ", err)
		return nil
	}

	klog.V(5).Info("Hub Subscription found Deployables:", dplList.Items)

	for _, dpl := range dplList.Items {
		if !checkDeployableBySubcriptionPackageFilter(sub, dpl) {
			continue
		}

		dplkey := types.NamespacedName{Name: dpl.Name, Namespace: dpl.Namespace}.String()
		allDpls[dplkey] = dpl.DeepCopy()
	}

	return allDpls
}

func checkDeployableBySubcriptionPackageFilter(sub *appv1alpha1.Subscription, dpl dplv1alpha1.Deployable) bool {
	if sub.Spec.PackageFilter != nil {
		if sub.Spec.Package != "" && sub.Spec.Package != dpl.Name {
			klog.V(5).Info("Name does not match, skiping:", sub.Spec.Package, "|", dpl.Name)
			return false
		}

		annotations := sub.Spec.PackageFilter.Annotations

		dplanno := dpl.GetAnnotations()
		if dplanno == nil {
			dplanno = make(map[string]string)
		}

		//append deployable template annotations to deployable annotations only if they don't exist in the deployable annotations
		dpltemplate := &unstructured.Unstructured{}

		if dpl.Spec.Template != nil {
			err := json.Unmarshal(dpl.Spec.Template.Raw, dpltemplate)
			if err == nil {
				dplTemplateAnno := dpltemplate.GetAnnotations()
				for k, v := range dplTemplateAnno {
					if dplanno[k] == "" {
						dplanno[k] = v
					}
				}
			}
		}

		vdpl := dpl.GetAnnotations()[dplv1alpha1.AnnotationDeployableVersion]

		klog.V(5).Info("checking annotations package filter: ", annotations)

		if annotations != nil {
			matched := true

			for k, v := range annotations {
				if dplanno[k] != v {
					matched = false
					break
				}
			}

			if !matched {
				return false
			}
		}

		vsub := sub.Spec.PackageFilter.Version
		if vsub != "" {
			vmatch := subutil.SemverCheck(vsub, vdpl)
			klog.V(5).Infof("version check is %v; subscription version filter condition is %v, deployable version is: %v", vmatch, vsub, vdpl)

			if !vmatch {
				return false
			}
		}
	}

	return true
}

// createTargetDplForRollingUpdate create a new deployable to contain the target subscription
func (r *ReconcileSubscription) createTargetDplForRollingUpdate(sub *appv1alpha1.Subscription) (*dplv1alpha1.Deployable, error) {
	annotations := sub.GetAnnotations()

	if annotations == nil || annotations[appv1alpha1.AnnotationRollingUpdateTarget] == "" {
		klog.V(5).Info("Empty annotation or No rolling update target in annotations", annotations)

		return nil, nil
	}

	targetSub := &appv1alpha1.Subscription{}
	targetSubKey := types.NamespacedName{
		Namespace: sub.Namespace,
		Name:      annotations[appv1alpha1.AnnotationRollingUpdateTarget],
	}
	err := r.Get(context.TODO(), targetSubKey, targetSub)

	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("target Subscription is gone: %#v.", targetSubKey)

			return nil, nil
		}
		// Error reading the object - requeue the request.
		klog.Infof("fetching target Subscription failed: %#v.", err)

		return nil, err
	}

	targetSubDpl, err := r.prepareDeployableForSubscription(targetSub, sub)

	if err != nil {
		klog.V(3).Infof("Prepare target Subscription deployable failed: %#v.", err)
		return nil, err
	}

	targetSubDpl.Name = sub.Name + "-target-deployable"
	targetSubDpl.Namespace = sub.Namespace

	err = r.updateTargetSubscriptionDeployable(sub, targetSubDpl)

	return targetSubDpl, err
}

func (r *ReconcileSubscription) updateTargetSubscriptionDeployable(sub *appv1alpha1.Subscription, targetSubDpl *dplv1alpha1.Deployable) error {
	targetKey := types.NamespacedName{
		Namespace: targetSubDpl.Namespace,
		Name:      targetSubDpl.Name,
	}

	found := &dplv1alpha1.Deployable{}
	err := r.Get(context.TODO(), targetKey, found)

	if err != nil && errors.IsNotFound(err) {
		klog.Info("Creating target Deployable - ", "namespace: ", targetSubDpl.Namespace, ", name: ", targetSubDpl.Name)
		err = r.Create(context.TODO(), targetSubDpl)

		//record events
		addtionalMsg := "target Depolyable " + targetKey.String() + " created in the subscription namespace"
		r.eventRecorder.RecordEvent(sub, "Deploy", addtionalMsg, err)

		return err
	} else if err != nil {
		return err
	}

	orgTpl := &unstructured.Unstructured{}
	err = json.Unmarshal(targetSubDpl.Spec.Template.Raw, orgTpl)

	if err != nil {
		klog.V(5).Info("Error in unmarshall target subscription deployable template, err:", err, " |template: ", string(targetSubDpl.Spec.Template.Raw))
		return err
	}

	fndTpl := &unstructured.Unstructured{}
	err = json.Unmarshal(found.Spec.Template.Raw, fndTpl)

	if err != nil {
		klog.V(5).Info("Error in unmarshall target found subscription deployable template, err:", err, " |template: ", string(found.Spec.Template.Raw))
		return err
	}

	if !reflect.DeepEqual(orgTpl, fndTpl) || !reflect.DeepEqual(targetSubDpl.Spec.Overrides, found.Spec.Overrides) {
		klog.V(5).Infof("Updating target Deployable. orig: %#v, found: %#v", targetSubDpl, found)

		targetSubDpl.Spec.DeepCopyInto(&found.Spec)

		foundanno := found.GetAnnotations()
		if foundanno == nil {
			foundanno = make(map[string]string)
		}

		foundanno[dplv1alpha1.AnnotationIsGenerated] = "true"
		foundanno[dplv1alpha1.AnnotationLocal] = "false"
		found.SetAnnotations(foundanno)

		klog.V(5).Info("Updating Deployable - ", "namespace: ", targetSubDpl.Namespace, " ,name: ", targetSubDpl.Name)

		err = r.Update(context.TODO(), found)

		//record events
		addtionalMsg := "target Depolyable " + targetKey.String() + " updated in the subscription namespace"
		r.eventRecorder.RecordEvent(sub, "Deploy", addtionalMsg, err)

		if err != nil {
			return err
		}
	}

	return nil
}
