build: 
	go build .

run: kind-create terraform-hack-init
	go run .

kind-create:
	@echo "Create cluster if doesn't exist"
	./scripts/init-kind-cluster.sh

terraform-hack-init:
	./hack/init.sh

integration-tests:
	# TODO automatically run the operator
	ginkgo --progress

create-rg:
	kubectl apply -f ./examples/resourceGroup.yaml

clear-rg:
	-kubectl patch resource-group/test1rg -p '{"metadata":{"finalizers":[]}}' --type=merge
	-kubectl delete -f ./examples/resourceGroup.yaml
	-az group delete --name test1 --yes

create-stor:
	kubectl apply -f ./examples/storageAccount.yaml

clear-stor:
	-kubectl patch storage-account/teststorage -p '{"metadata":{"finalizers":[]}}' --type=merge
	-kubectl delete -f ./examples/storageAccount.yaml
	-az storage account delete --name test14tfbop --yes
