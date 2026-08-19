#!/bin/sh
cd "$(dirname "$0")"
if [ ! -f proxypool ]; then
  echo "==> 未找到 proxypool，请先执行 make build"
  exit 1
fi
./proxypool --port=1080 --conf=clash.yaml