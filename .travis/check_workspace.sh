#!/bin/sh
set -e

if [ "$(git status --porcelain | wc -l)" -ne "0" ]; then 
  echo "Unclean workspace detected. This typically indicates 'gofmt' changed some files." \
       "\n Please run 'make fmt' locally to verify." 
  exit 1
else
  exit 0
fi
