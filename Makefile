
run: kind-create
	go run .

kind-create:
	@echo "Create cluster if doesn't exist"
	-kind create cluster --name tob
	kind export kubeconfig --name tob