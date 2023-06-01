/*
Copyright 2023 The Crossplane Authors.

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

// Package usage contains the handler for the usage webhook.
package usage

import (
	"context"
	"errors"
	"fmt"
	xpunstructured "github.com/crossplane/crossplane-runtime/pkg/resource/unstructured"
	"github.com/crossplane/crossplane/apis/apiextensions/v1alpha1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
)

const (
	// key used to index CRDs by "Kind" and "group", to be used when
	// indexing and retrieving needed CRDs
	usingIndexKey = "using.apiversion.kind.name"
)

// handler implements the admission handler for Composition.
type handler struct {
	reader  client.Reader
	options controller.Options
}

func getIndexValueForUsage(u *v1alpha1.Usage) string {
	return fmt.Sprintf("%s.%s.%s", u.Spec.Of.APIVersion, u.Spec.Of.Kind, u.Spec.Of.Name)
}
func getIndexValueForObject(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s.%s.%s", u.GetAPIVersion(), u.GetKind(), u.GetName())
}

// SetupWebhookWithManager sets up the webhook with the manager.
func SetupWebhookWithManager(mgr ctrl.Manager, options controller.Options) error {

	indexer := mgr.GetFieldIndexer()
	if err := indexer.IndexField(context.Background(), &v1alpha1.Usage{}, usingIndexKey, func(obj client.Object) []string {
		return []string{getIndexValueForUsage(obj.(*v1alpha1.Usage))}
	}); err != nil {
		return err
	}

	mgr.GetWebhookServer().Register("/validate-no-usages",
		&webhook.Admission{Handler: &handler{
			reader:  xpunstructured.NewClient(mgr.GetClient()),
			options: options,
		}})

	return nil
}

// Handle handles the admission request, validating the Composition.
func (h *handler) Handle(ctx context.Context, request admission.Request) admission.Response {
	switch request.Operation {
	case admissionv1.Create, admissionv1.Update, admissionv1.Connect:
		return admission.Errored(http.StatusBadRequest, errors.New("unexpected operation"))
	case admissionv1.Delete:
		u := &unstructured.Unstructured{}
		if err := u.UnmarshalJSON(request.OldObject.Raw); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		return h.validateNoUsages(ctx, u)
	default:
		return admission.Errored(http.StatusBadRequest, errors.New("unexpected operation"))
	}
}

func (h *handler) validateNoUsages(ctx context.Context, u *unstructured.Unstructured) admission.Response {
	fmt.Println("Checking for usages")

	usageList := &v1alpha1.UsageList{}
	if err := h.reader.List(ctx, usageList, client.MatchingFields{usingIndexKey: getIndexValueForObject(u)}); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	for _, usage := range usageList.Items {
		if usage.Spec.Of.APIVersion == u.GetAPIVersion() && usage.Spec.Of.Kind == u.GetKind() && usage.Spec.Of.Name == u.GetName() {
			return admission.Denied(fmt.Sprintf("The resource is used by %s/%s", u.GroupVersionKind().String(), usage.Spec.By.Name))
		}
	}
	return admission.Allowed("")
}
