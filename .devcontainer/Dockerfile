FROM golang:1.14.4-buster

# Avoid warnings by switching to noninteractive
ENV DEBIAN_FRONTEND=noninteractive

# This Dockerfile adds a non-root 'vscode' user with sudo access. However, for Linux,
# this user's GID/UID must match your local user UID/GID to avoid permission issues
# with bind mounts. Update USER_UID / USER_GID if yours is not 1000. See
# https://aka.ms/vscode-remote/containers/non-root-user for details.
ARG USERNAME=vscode
ARG USER_UID=1000
ARG USER_GID=$USER_UID

ENV GO111MODULE=on

# Configure apt, install packages and tools
RUN apt-get update \
    && apt-get -y install --no-install-recommends apt-utils dialog fuse nano xterm locales unzip \
    #
    # Verify git, process tools, lsb-release (common in install instructions for CLIs) installed
    && apt-get -y install git iproute2 procps lsb-release \
    #
    # Install Azure CLI
    && curl -sL https://aka.ms/InstallAzureCLIDeb | bash \
    #
    # Install Integration testing Tools 
    #
    # --> xvfb for integration testing (gocui requires a valid tty which isn't available in ci)
    && apt-get install --no-install-recommends -y xvfb libgl1-mesa-dri \
    # 
    # Install Release Tools
    #
    # --> RPM used by goreleaser
    && apt install -y rpm \
    # Install python3
    && apt-get -y install python3-pip \
    && apt-get -y install bash-completion \
    # Install shellcheck
    && apt-get -y install shellcheck

# Setup locale as required by snapd: https://stackoverflow.com/questions/28405902/how-to-set-the-locale-inside-a-debian-ubuntu-docker-container
RUN sed -i -e 's/# en_US.UTF-8 UTF-8/en_US.UTF-8 UTF-8/' /etc/locale.gen && \
    dpkg-reconfigure --frontend=noninteractive locales && \
    update-locale LANG=en_US.UTF-8

ENV GIT_PROMPT_START='\033[1;36mtob-devcon>\033[0m\033[0;33m\w\a\033[0m'

# Save command line history 
RUN echo "export HISTFILE=/root/commandhistory/.bash_history" >> "/root/.bashrc" \
    && echo "export PROMPT_COMMAND='history -a'" >> "/root/.bashrc" \
    && mkdir -p /root/commandhistory \
    && touch /root/commandhistory/.bash_history

RUN echo "source /usr/share/bash-completion/bash_completion" >> "/root/.bashrc"

# Git command prompt
RUN git clone https://github.com/magicmonty/bash-git-prompt.git ~/.bash-git-prompt --depth=1 \
    && echo "if [ -f \"$HOME/.bash-git-prompt/gitprompt.sh\" ]; then GIT_PROMPT_ONLY_IN_REPO=1 && source $HOME/.bash-git-prompt/gitprompt.sh; fi" >> "/root/.bashrc"

ENV DEVCONTAINER="TRUE"

# Install docker used by go releaser
RUN bash -c "cd /tmp && curl -fsSLO https://download.docker.com/linux/static/stable/x86_64/docker-19.03.5.tgz && tar --strip-components=1 -xvzf docker-19.03.5.tgz -C /usr/local/bin"

# Install mdspell 
RUN \
    apt-get install -y nodejs npm \
    && npm i markdown-spellcheck -g

# Install Go tools
RUN \
    # --> Delve for debugging
    go get github.com/go-delve/delve/cmd/dlv@v1.4.1 \
    # --> Go language server
    && go get golang.org/x/tools/gopls@v0.4.1 \
    # --> Go symbols and outline for go to symbol support and test support 
    && go get github.com/acroca/go-symbols@v0.1.1 && go get github.com/ramya-rao-a/go-outline@7182a932836a71948db4a81991a494751eccfe77 \
    # --> GolangCI-lint
    && curl -sfL https://install.goreleaser.com/github.com/golangci/golangci-lint.sh | sed 's/tar -/tar --no-same-owner -/g' | sh -s -- -b $(go env GOPATH)/bin \
    # --> Install Ginkgo
    && go get github.com/onsi/ginkgo/ginkgo@v1.12.0  \
    # --> Go releaser 
    && curl -sfL https://install.goreleaser.com/github.com/goreleaser/goreleaser.sh | sh -s -- "v0.132.1"\
    # --> Go rich output for testing with colors
    && go get github.com/kyoh86/richgo@v0.3.3 \
    && rm -rf /go/src/ && rm -rf /go/pkg

RUN echo "alias go=richgo" >> "/root/.bashrc"

ARG TERRAFORM_VERSION=0.12.26
RUN \
    # Install Terraform
    mkdir -p /tmp/docker-downloads \
    && curl -sSL -o /tmp/docker-downloads/terraform.zip https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_amd64.zip \
    && unzip /tmp/docker-downloads/terraform.zip \
    && mv terraform /usr/local/bin

# Install KIND
RUN \
    curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.8.1/kind-$(uname)-amd64 \
    && chmod +x ./kind \
    && mv ./kind /usr/local/bin/kind

# Install Kubectl 
RUN \
    curl -LO https://storage.googleapis.com/kubernetes-release/release/v1.18.0/bin/linux/amd64/kubectl \
    && chmod +x ./kubectl \
    && mv ./kubectl /usr/local/bin/kubectl

# Install Ngrok
RUN \
    curl -LO https://bin.equinox.io/c/4VmDzA7iaHb/ngrok-stable-linux-amd64.zip \
    && unzip ./ngrok-stable-linux-amd64.zip \
    && mv ngrok /usr/local/bin