#!/bin/bash

echo "[TAG] ------------------------------------------------------"

# shellcheck disable=SC2162
read -p "[TAG] enter new tag(eg. 1.3.14):" number

echo ""


echo "[TAG ${number}] components/cron"
git tag -a "cron/v${number}" -m "auto tag"


echo "[TAG ${number}] components/data-config"
git tag -a "data-config/v${number}" -m "auto tag"


echo "[TAG ${number}] components/etcd"
git tag -a "etcd/v${number}" -m "auto tag"


echo "[TAG ${number}] components/gin"
git tag -a "gin/v${number}" -m "auto tag"


echo "[TAG ${number}] components/gops"
git tag -a "gops/v${number}" -m "auto tag"


echo "[TAG ${number}] components/gorm"
git tag -a "gorm/v${number}" -m "auto tag"


echo "[TAG ${number}] components/mongo"
git tag -a "mongo/v${number}" -m "auto tag"

echo "[TAG] ------------------------------------------------------"