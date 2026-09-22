#!/usr/bin/env bash
set -euo pipefail

exec 3<>/dev/tcp/127.0.0.1/5005
printf 'POST / HTTP/1.1\r\nHost: localhost\r\nContent-Length: 2\r\nContent-Type: application/json\r\nConnection: close\r\n\r\n{}' >&3
IFS= read -r status <&3
[[ "$status" == HTTP/* ]]
