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

// Package usage manages the lifecycle of Usage objects.
package usage

import (
	"context"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/resource/unstructured"
	"github.com/crossplane/crossplane/internal/controller/apiextensions/usage/dependency"

	"github.com/crossplane/crossplane/apis/apiextensions/v1alpha1"
	apiextensionscontroller "github.com/crossplane/crossplane/internal/controller/apiextensions/controller"
)

const (
	timeout   = 2 * time.Minute
	finalizer = "usage.apiextensions.crossplane.io"

	errGetUsage          = "cannot get usage"
	errGetUsing          = "cannot get using"
	errGetUsed           = "cannot get used"
	errBlock             = "cannot add blocker annotation"
	errUnblock           = "cannot remove from blocker annotation"
	errAddOwnerReference = "cannot add owner reference"
	errAddFinalizer      = "cannot add composite resource finalizer"
	errRemoveFinalizer   = "cannot remove composite resource finalizer"
)

// Setup adds a controller that reconciles Usages by
// defining a composite resource and starting a controller to reconcile it.
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

// NewReconciler returns a Reconciler of Usages.
func NewReconciler(mgr manager.Manager, opts ...ReconcilerOption) *Reconciler {
	kube := unstructured.NewClient(mgr.GetClient())

	r := &Reconciler{
		mgr: mgr,

		client: resource.ClientApplicator{
			Client:     kube,
			Applicator: resource.NewAPIUpdatingApplicator(kube),
		},

		usage: resource.NewAPIFinalizer(kube, finalizer),

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
	client resource.ClientApplicator
	mgr    manager.Manager

	usage resource.Finalizer

	log    logging.Logger
	record event.Recorder

	pollInterval time.Duration
}

// Reconcile a Usage by defining a new kind of composite
// resource and starting a controller to reconcile it.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) { //nolint:gocyclo // Reconcilers are complex. Be wary of adding more.
	log := r.log.WithValues("request", req)
	log.Debug("Reconciling Usage")

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Get the Usage resource for this request.
	u := &v1alpha1.Usage{}
	if err := r.client.Get(ctx, req.NamespacedName, u); err != nil {
		log.Debug(errGetUsage, "error", err)
		return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), errGetUsage)
	}

	log = log.WithValues(
		"uid", u.GetUID(),
		"version", u.GetResourceVersion(),
		"name", u.GetName(),
	)

	// Identify using resource as an unstructured object.
	using := dependency.New(dependency.FromReference(v1.ObjectReference{
		Kind:       u.Spec.By.Kind,
		Name:       u.Spec.By.Name,
		APIVersion: u.Spec.By.APIVersion,
		UID:        u.Spec.By.UID,
	}))

	if meta.WasDeleted(u) {
		// Get the using resource
		err := r.client.Get(ctx, client.ObjectKey{Name: u.Spec.By.Name}, using)
		if resource.IgnoreNotFound(err) != nil {
			log.Debug(errGetUsing, "error", err)
			return reconcile.Result{}, errors.Wrap(resource.IgnoreNotFound(err), errGetUsing)
		}
		if err == nil {
			// If the using resource is not deleted, we must wait for it to be deleted
			log.Debug("Using resource is not deleted, waiting")
			return reconcile.Result{RequeueAfter: 1 * time.Minute}, nil
		}
		// Using resource is deleted, we can proceed with the deletion of the usage

		// Remove the finalizer from the usage
		if err = r.usage.RemoveFinalizer(ctx, u); err != nil {
			log.Debug(errRemoveFinalizer, "error", err)
			return reconcile.Result{}, errors.Wrap(err, errRemoveFinalizer)
		}

		return reconcile.Result{}, nil
	}

	// Get the using resource
	if err := r.client.Get(ctx, client.ObjectKey{Name: u.Spec.By.Name}, using); err != nil {
		log.Debug(errGetUsing, "error", err)
		return reconcile.Result{}, errors.Wrap(err, errGetUsing)
	}

	// Usage should have a finalizer and be owned by the using resource.
	if owners := u.GetOwnerReferences(); len(owners) == 0 || owners[0].UID != using.GetUID() {
		u.Finalizers = []string{finalizer}
		u.SetOwnerReferences([]metav1.OwnerReference{meta.AsOwner(
			meta.TypedReferenceTo(using, using.GetObjectKind().GroupVersionKind()),
		)})
		u.Spec.By.UID = using.GetUID()
		if err := r.client.Update(ctx, u); err != nil {
			log.Debug(errAddOwnerReference, "error", err)
			return reconcile.Result{}, err
		}
	}

	return reconcile.Result{}, nil
}
