package main

import (
	"context"
	"log"
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
			log.Printf("*** Handling Add: Namespace=%s; Kind=%s; Name=%s\n", resource.GetNamespace(), resource.GetKind(), resource.GetName())
			reconcileCrd(provider, resource.GetKind(), resource)

			gvr := resource.GroupVersionKind().GroupVersion().WithResource(resource.GetKind() + "s") // TODO - look at a better way of getting this!
			options := v1.UpdateOptions{}
			newResource, err := clientSet.Resource(gvr).Namespace(resource.GetNamespace()).Update(context.TODO(), resource, options)
			if err != nil {
				log.Println(err)
				panic("Error updating CRD instance (Add)")
			}
			_ = newResource
		},
		// When a pod resource updated
		UpdateFunc: func(oldObj interface{}, newObj interface{}) {
			oldResource := oldObj.(*unstructured.Unstructured)
			oldGen := oldResource.GetGeneration()
			resource := newObj.(*unstructured.Unstructured)
			gen := resource.GetGeneration()
			log.Printf("*** Handling Add: Namespace=%s; Kind=%s; Name=%s\n", resource.GetNamespace(), resource.GetKind(), resource.GetName())
			if oldGen == gen {
				log.Printf("Generation hasn't changed (%d) - skipping event\n", gen)
				return
			}
			reconcileCrd(provider, resource.GetKind(), resource)

			gvr := resource.GroupVersionKind().GroupVersion().WithResource(resource.GetKind() + "s") // TODO - look at a better way of getting this!
			options := v1.UpdateOptions{}
			newResource, err := clientSet.Resource(gvr).Namespace(resource.GetNamespace()).Update(context.TODO(), resource, options)
			if err != nil {
				log.Println(err)
				panic("Error updating CRD instance (Update)")
			}
			_ = newResource
		},
		// When a pod resource deleted
		DeleteFunc: func(interface{}) { panic("not implemented") },
	})

	stopCh := make(chan struct{}, 1)
	informer.Run(stopCh)

}
