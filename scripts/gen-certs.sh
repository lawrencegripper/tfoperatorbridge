#! /bin/bash
cd "$(dirname "$0")"

# Todo: inject ip address of host

if [ -d "../certs" ]; then
    echo "../certs exists so assuming certs gen'd already."
    exit 0
fi

set -e

mkdir -p ../certs
cd ../certs

openssl genrsa -out ca.key 2048
openssl req -x509 -new -nodes -key ca.key -days 100000 -out ca.crt -subj "/CN=admission_ca"

cat >tls.conf <<EOF
[req]
req_extensions = v3_req
distinguished_name = req_distinguished_name
[req_distinguished_name]
[ v3_req ]
basicConstraints = CA:FALSE
keyUsage = nonRepudiation, digitalSignature, keyEncipherment
extendedKeyUsage = clientAuth, serverAuth
subjectAltName = @alt_names
[req_ext]
subjectAltName = @alt_names
[alt_names]
IP.1 = 10.0.1.13
EOF

openssl genrsa -out tls.key 2048
openssl req -new -key tls.key -out tls.csr -subj "/IP=10.0.1.13" -config tls.conf
openssl x509 -req -in tls.csr -CA ca.crt -CAkey ca.key -CAcreateserial -out tls.crt -days 100000 -extensions v3_req -extfile tls.conf

