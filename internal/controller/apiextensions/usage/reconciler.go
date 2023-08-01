/*
Copyright 2020 The Crossplane Authors.

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

// Package usage manages the lifecycle of usageResource objects.
package usage

import (
	"context"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	xpresource "github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/resource/unstructured"

	"github.com/crossplane/crossplane/apis/apiextensions/v1alpha1"
	apiextensionscontroller "github.com/crossplane/crossplane/internal/controller/apiextensions/controller"
	"github.com/crossplane/crossplane/internal/controller/apiextensions/usage/resource"
	"github.com/crossplane/crossplane/internal/usage"
	"github.com/crossplane/crossplane/internal/xcrd"
)

const (
	timeout   = 2 * time.Minute
	finalizer = "usage.apiextensions.crossplane.io"
	// Note(turkenh): In-use label enables the "DELETE" requests on resources
	// with this label to be intercepted by the webhook and rejected if the
	// xpresource is in use.
	inUseLabelKey = "crossplane.io/in-use"

	errGetUsage         = "cannot get usage"
	errResolveSelectors = "cannot resolve selectors"
	errListUsages       = "cannot list usages"
	errGetUsing         = "cannot get using"
	errGetUsed          = "cannot get used"
	errAddOwnerToUsage  = "cannot update usage xpresource with owner ref"
	errAddInUseLabel    = "cannot add in use use label to the used xpresource"
	errRemoveInUseLabel = "cannot remove in use label from the used xpresource"
	errAddFinalizer     = "cannot add finalizer"
	errRemoveFinalizer  = "cannot remove finalizer"
	errUpdateStatus     = "cannot update status of usage"
)

// Event reasons.
const (
	reasonResolveSelectors event.Reason = "ResolveSelectors"
	reasonListUsages       event.Reason = "ListUsages"
	reasonGetUsed          event.Reason = "GetUsedResource"
	reasonGetUsing         event.Reason = "GetUsingResource"
	reasonOwnerRefToUsage  event.Reason = "AddOwnerRefToUsage"
	reasonAddInUseLabel    event.Reason = "AddInUseLabel"
	reasonRemoveInUseLabel event.Reason = "RemoveInUseLabel"
	reasonAddFinalizer     event.Reason = "AddFinalizer"
	reasonRemoveFinalizer  event.Reason = "RemoveFinalizer"

	reasonUsageConfigured event.Reason = "UsageConfigured"
	reasonWaitUsing       event.Reason = "WaitingUsingDeleted"
)

type selectorResolver interface {
	resolveSelectors(ctx context.Context, u *v1alpha1.Usage) error
}

// Setup adds a controller that reconciles Usages by
// defining a composite xpresource and starting a controller to reconcile it.
func Setup(mgr ctrl.Manager, o apiextensionscontroller.Options) error {
	name := "usage/" + strings.ToLower(v1alpha1.UsageGroupKind)
	r := NewReconciler(mgr,
		WithLogger(o.Logger.WithValues("controller", name)),
		WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Usage{}).
		WithOptions(o.ForControllerRuntime()).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

// ReconcilerOption is used to configure the Reconciler.
type ReconcilerOption func(*Reconciler)

// WithLogger specifies how the Reconciler should log messages.
func WithLogger(log logging.Logger) ReconcilerOption {
	return func(r *Reconciler) {
		r.log = log
	}
}

// WithRecorder specifies how the Reconciler should record Kubernetes events.
func WithRecorder(er event.Recorder) ReconcilerOption {
	return func(r *Reconciler) {
		r.record = er
	}
}

type usageResource struct {
	xpresource.Finalizer
	selectorResolver
}

// NewReconciler returns a Reconciler of Usages.
func NewReconciler(mgr manager.Manager, opts ...ReconcilerOption) *Reconciler {
	kube := unstructured.NewClient(mgr.GetClient())

	r := &Reconciler{
		mgr: mgr,

		client: xpresource.ClientApplicator{
			Client:     kube,
			Applicator: xpresource.NewAPIUpdatingApplicator(kube),
		},

		usage: usageResource{
			Finalizer:        xpresource.NewAPIFinalizer(kube, finalizer),
			selectorResolver: newAPISelectorResolver(kube),
		},

		log:    logging.NewNopLogger(),
		record: event.NewNopRecorder(),

		pollInterval: 30 * time.Second,
	}

	for _, f := range opts {
		f(r)
	}
	return r
}

// A Reconciler reconciles Usages.
type Reconciler struct {
	client xpresource.ClientApplicator
	mgr    manager.Manager

	usage usageResource

	log    logging.Logger
	record event.Recorder

	pollInterval time.Duration
}

// Reconcile a usageResource by defining a new kind of composite
// xpresource and starting a controller to reconcile it.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) { //nolint:gocyclo // Reconcilers are typically complex.
	log := r.log.WithValues("request", req)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get the usageResource xpresource for this request.
	u := &v1alpha1.Usage{}
	if err := r.client.Get(ctx, req.NamespacedName, u); err != nil {
		log.Debug(errGetUsage, "error", err)
		return reconcile.Result{}, errors.Wrap(xpresource.IgnoreNotFound(err), errGetUsage)
	}

	if err := r.usage.resolveSelectors(ctx, u); err != nil {
		log.Debug(errResolveSelectors, "error", err)
		err = errors.Wrap(err, errResolveSelectors)
		r.record.Event(u, event.Warning(reasonResolveSelectors, err))
		return reconcile.Result{}, err
	}

	r.record.Event(u, event.Normal(reasonResolveSelectors, "Selectors resolved, if any."))

	of := u.Spec.Of
	by := u.Spec.By

	// Identify used xp resource as an unstructured object.
	used := resource.New(resource.FromReference(v1.ObjectReference{
		Kind:       of.Kind,
		Name:       of.ResourceRef.Name,
		APIVersion: of.APIVersion,
	}))

	if meta.WasDeleted(u) {
		if by != nil {
			// Identify using xpresource as an unstructured object.
			using := resource.New(resource.FromReference(v1.ObjectReference{
				Kind:       by.Kind,
				Name:       by.ResourceRef.Name,
				APIVersion: by.APIVersion,
			}))
			// Get the using xpresource
			err := r.client.Get(ctx, client.ObjectKey{Name: by.ResourceRef.Name}, using)
			if xpresource.IgnoreNotFound(err) != nil {
				log.Debug(errGetUsing, "error", err)
				err = errors.Wrap(xpresource.IgnoreNotFound(err), errGetUsing)
				r.record.Event(u, event.Warning(reasonGetUsing, err))
				return reconcile.Result{}, err
			}

			if l := u.GetLabels()[xcrd.LabelKeyNamePrefixForComposed]; len(l) > 0 && l == using.GetLabels()[xcrd.LabelKeyNamePrefixForComposed] && err == nil {
				// If the usage and using xpresource are part of the same composite xpresource, we need to wait for the using xpresource to be deleted
				msg := "Waiting for using xpresource to be deleted."
				log.Debug(msg)
				r.record.Event(u, event.Normal(reasonWaitUsing, msg))
				return reconcile.Result{RequeueAfter: 30 * time.Second}, nil
			}
		}
		// At this point using xpresource is either:
		// - not defined
		// - not found (deleted)
		// - not part of the same composite xpresource
		// So, we can proceed with the deletion of the usage.

		// Get the used xpresource
		var err error
		if err = r.client.Get(ctx, client.ObjectKey{Name: of.ResourceRef.Name}, used); xpresource.IgnoreNotFound(err) != nil {
			log.Debug(errGetUsed, "error", err)
			err = errors.Wrap(err, errGetUsed)
			r.record.Event(u, event.Warning(reasonGetUsed, err))
			return reconcile.Result{}, err
		}

		// Remove the in-use label from the used xpresource if no other usages
		// exists.
		if err == nil {
			usageList := &v1alpha1.UsageList{}
			if err = r.client.List(ctx, usageList, client.MatchingFields{usage.InUseIndexKey: usage.IndexValueForObject(used.GetUnstructured())}); err != nil {
				log.Debug(errListUsages, "error", err)
				err = errors.Wrap(err, errListUsages)
				r.record.Event(u, event.Warning(reasonListUsages, err))
				return reconcile.Result{}, errors.Wrap(err, errListUsages)
			}
			// There are no "other" usageResource's referencing the used xpresource,
			// so we can remove the in-use label from the used xpresource
			if len(usageList.Items) < 2 {
				meta.RemoveLabels(used, inUseLabelKey)
				if err = r.client.Update(ctx, used); err != nil {
					log.Debug(errRemoveInUseLabel, "error", err)
					err = errors.Wrap(err, errRemoveInUseLabel)
					r.record.Event(u, event.Warning(reasonRemoveInUseLabel, err))
					return reconcile.Result{}, err
				}
			}
		}

		// Remove the finalizer from the usage
		if err = r.usage.RemoveFinalizer(ctx, u); err != nil {
			log.Debug(errRemoveFinalizer, "error", err)
			err = errors.Wrap(err, errRemoveFinalizer)
			r.record.Event(u, event.Warning(reasonRemoveFinalizer, err))
			return reconcile.Result{}, err
		}

		return reconcile.Result{}, nil
	}

	// Add finalizer for Usage xpresource.
	if err := r.usage.AddFinalizer(ctx, u); err != nil {
		log.Debug(errAddFinalizer, "error", err)
		err = errors.Wrap(err, errAddFinalizer)
		r.record.Event(u, event.Warning(reasonAddFinalizer, err))
		return reconcile.Result{}, nil
	}

	// Get the used xpresource
	if err := r.client.Get(ctx, client.ObjectKey{Name: of.ResourceRef.Name}, used); err != nil {
		log.Debug(errGetUsed, "error", err)
		err = errors.Wrap(err, errGetUsed)
		r.record.Event(u, event.Warning(reasonGetUsed, err))
		return reconcile.Result{}, err
	}

	// Used xpresource should have in-use label.
	if used.GetLabels()[inUseLabelKey] != "true" || !used.OwnedBy(u.GetUID()) {
		// Note(turkenh): Composite controller will not remove this label with
		// new reconciles since it uses a patching applicator to update the
		// xpresource.
		meta.AddLabels(used, map[string]string{inUseLabelKey: "true"})
		if err := r.client.Update(ctx, used); err != nil {
			log.Debug(errAddInUseLabel, "error", err)
			err = errors.Wrap(err, errAddInUseLabel)
			r.record.Event(u, event.Warning(reasonAddInUseLabel, err))
			return reconcile.Result{}, err
		}
	}

	if by != nil {
		// Identify using xpresource as an unstructured object.
		using := resource.New(resource.FromReference(v1.ObjectReference{
			Kind:       by.Kind,
			Name:       by.ResourceRef.Name,
			APIVersion: by.APIVersion,
		}))

		// Get the using xpresource
		if err := r.client.Get(ctx, client.ObjectKey{Name: by.ResourceRef.Name}, using); err != nil {
			log.Debug(errGetUsing, "error", err)
			err = errors.Wrap(err, errGetUsing)
			r.record.Event(u, event.Warning(reasonGetUsing, err))
			return reconcile.Result{}, err
		}

		// usageResource should have a finalizer and be owned by the using xpresource.
		if owners := u.GetOwnerReferences(); len(owners) == 0 || owners[0].UID != using.GetUID() {
			meta.AddOwnerReference(u, meta.AsOwner(
				meta.TypedReferenceTo(using, using.GetObjectKind().GroupVersionKind()),
			))
			if err := r.client.Update(ctx, u); err != nil {
				log.Debug(errAddOwnerToUsage, "error", err)
				err = errors.Wrap(err, errAddOwnerToUsage)
				r.record.Event(u, event.Warning(reasonOwnerRefToUsage, err))
				return reconcile.Result{}, err
			}
		}
	}

	// Note(turkenh): Do we really need a Synced condition? Maybe just to be
	// consistent with the other XP resources.
	u.Status.SetConditions(xpv1.ReconcileSuccess())
	u.Status.SetConditions(xpv1.Available())
	r.record.Event(u, event.Normal(reasonUsageConfigured, "Usage configured successfully."))
	return reconcile.Result{}, errors.Wrap(r.client.Status().Update(ctx, u), errUpdateStatus)
}
