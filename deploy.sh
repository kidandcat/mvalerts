#!/bin/bash
GOOS=linux GOARCH=amd64 go build .
scp -i ~/.ssh/id_rsa_kidandcat -P 1000 mvalerts root@galax.be:/root/mvalerts