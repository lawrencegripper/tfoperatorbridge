package main

import (
	"log"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"
)

func startSharedInformer() {
	clientConfig := getK8sClientConfig()

	clientSet, err := dynamic.NewForConfig(clientConfig)
	if err != nil {
		log.Panicln(err)
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

			log.Println(resource)
		},
		// When a pod resource updated
		UpdateFunc: func(interface{}, interface{}) { panic("not implemented") },
		// When a pod resource deleted
		DeleteFunc: func(interface{}) { panic("not implemented") },
	})

	stopCh := make(chan struct{}, 1)
	informer.Run(stopCh)

}
