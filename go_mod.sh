#!/bin/bash

cd cron
go mod tidy
cd ..

cd data-config
go mod tidy
cd ..

cd etcd
go mod tidy
cd ..

cd gin
go mod tidy
cd ..

cd gops
go mod tidy
cd ..

cd gorm
go mod tidy
cd ..

cd mongo
go mod tidy
cd ..