package main

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/hashicorp/terraform/plugin"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

func startSharedInformer(provider *plugin.GRPCProvider) {
	clientConfig := getK8sClientConfig()

	clientSet, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		log.Println(err)
		panic("Failed to create k8s client set")
	}

	factory := dynamicinformer.NewDynamicSharedInformerFactory(clientSet, time.Minute*5)

	// Todo: Register informers for all the resources... Ideally one single informer for all in group `azurerm.tfb.local`
	informerGeneric := factory.ForResource(schema.GroupVersionResource{
		Group:    "azurerm.tfb.local",
		Version:  "valpha1",
		Resource: "resource-groups",
	})

	informer := informerGeneric.Informer()

	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		// When a new resource gets created
		AddFunc: func(obj interface{}) {
			resource := obj.(*unstructured.Unstructured)
			gen := resource.GetGeneration()
			log.Printf("*** Handling Add: Namespace=%s; Kind=%s; Name=%s (Generation=%d)\n", resource.GetNamespace(), resource.GetKind(), resource.GetName(), gen)
			if !generationHasChanged(resource, nil) {
				return
			}

			reconcileCrd(provider, resource.GetKind(), resource)

			_, err := saveResource(clientSet, resource)
			if err != nil {
				log.Println(err)
				panic("Error updating CRD instance (Add)") // TODO handle retries
			}
		},
		// When a pod resource updated or marked for deletion
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			oldResource := oldObj.(*unstructured.Unstructured)
			resource := newObj.(*unstructured.Unstructured)
			gen := resource.GetGeneration()

			log.Printf("*** Handling Update: Namespace=%s; Kind=%s; Name=%s (Generation=%d)\n", resource.GetNamespace(), resource.GetKind(), resource.GetName(), gen)
			if !generationHasChanged(resource, oldResource) {
				return
			}

			reconcileCrd(provider, resource.GetKind(), resource)

			_, err := saveResource(clientSet, resource)
			if err != nil {
				log.Println(err)
				panic("Error updating CRD instance (Update)") // TODO handle retries
			}
		},
		// Not currently using delete as we add a finalizer so deletes are signalled via updates with a deletion timestamp
		// DeleteFunc: func(interface{}) { panic("not implemented") },
	})

	stopCh := make(chan struct{}, 1)
	informer.Run(stopCh)
}

func generationHasChanged(currentResource *unstructured.Unstructured, oldResource *unstructured.Unstructured) bool {
	currentGeneration := currentResource.GetGeneration()

	if oldResource != nil {
		oldGeneration := oldResource.GetGeneration()
		if oldGeneration == currentGeneration {
			log.Printf("Generation hasn't changed (%d) - skipping event\n", currentGeneration)
			return false
		}
	}

	var lastAppliedGeneration int
	var err error
	annotations := currentResource.GetAnnotations()
	if s := annotations["lastAppliedGeneration"]; s != "" {
		lastAppliedGeneration, err = strconv.Atoi(s)
		if err != nil {
			log.Printf("Unable to parse lastAppliedGeneration %q: %s", s, err)
			return true
		}
	}
	if lastAppliedGeneration == int(currentGeneration) {
		log.Printf("Generation matches LastAppliedGeneration (%d) - skipping event\n", currentGeneration)
		return false
	}

	return true
}

func saveResource(clientSet dynamic.Interface, resource *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	gvr := resource.GroupVersionKind().GroupVersion().WithResource(resource.GetKind() + "s") // TODO - look at a better way of getting this!
	options := v1.UpdateOptions{}
	newResource, err := clientSet.Resource(gvr).Namespace(resource.GetNamespace()).Update(context.TODO(), resource, options)
	if err != nil {
		return nil, err
	}
	return newResource, nil
}
