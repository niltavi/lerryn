#!/bin/sh
# 生成标准拓扑参考部署用到的口令文件和目录。已存在的文件不会被覆盖，可以重复执行。
# 需要 openssl。创建属于 uid 65532 的目录时需要 root 权限。macOS 上改在一个临时容器里执行，不需要 sudo。
set -eu

cd "$(dirname "$0")"

# macOS 上 Docker 跑在 OrbStack 的 Linux 虚拟机里：/var/lib 下的 runner 数据目录在虚拟机里，
# 不在 Mac 上，在 Mac 上 chown 给 65532 又需要 sudo。所以改在一个临时容器里以 root
# 执行同一个脚本，挂载虚拟机的 /var/lib。
if [ "$(uname -s)" = Darwin ] && [ -z "${LERRYN_INIT_IN_CONTAINER:-}" ]; then
	exec docker run --rm -e LERRYN_INIT_IN_CONTAINER=1 \
		-v "$PWD:$PWD" -w "$PWD" -v /var/lib:/var/lib \
		alpine:3.22 sh -c 'apk add -q openssl >/dev/null && ./init-secrets.sh'
fi

umask 077
mkdir -p secrets

gen() {
	# $1 文件名，$2 内容
	if [ ! -e "secrets/$1" ]; then
		printf '%s' "$2" > "secrets/$1"
		echo "created secrets/$1"
	fi
}

pg_password=$(openssl rand -hex 24)
gen postgres-password "$pg_password"
# pgpass 中的口令必须与 postgres-password 一致；两者都已存在时不再改动。
if [ ! -e secrets/pgpass ]; then
	printf 'postgres:5432:lerryn:lerryn:%s\n' "$(cat secrets/postgres-password)" > secrets/pgpass
	echo "created secrets/pgpass"
fi
gen s3-access-key-id "lerryn"
gen s3-secret-access-key "$(openssl rand -hex 24)"
# runner 的注册令牌。server 经 server.runner_registration_token_file 接受它，runner 首次启动时用它换取自己的凭证。
gen runner-registration-token "$(openssl rand -hex 32)"

# 容器里的进程以 uid 65532 运行，读取挂载进去的口令文件需要读权限。
# secrets/ 目录本身保持 0700，宿主机上的其他用户无法进入。
chmod 0700 secrets
chmod 0644 secrets/postgres-password secrets/pgpass secrets/s3-access-key-id secrets/s3-secret-access-key secrets/runner-registration-token

# 主密钥目录：第一个 server 启动时在这里生成主密钥，所有 server 共用。
mkdir -p secrets/master
# runner 的数据目录：容器内外路径一致。
for d in secrets/master /var/lib/lerryn-runner-1 /var/lib/lerryn-runner-2; do
	mkdir -p "$d" 2>/dev/null || sudo mkdir -p "$d"
	if [ "$(id -u)" -eq 0 ]; then
		chown 65532:65532 "$d"
	else
		sudo chown 65532:65532 "$d"
	fi
done
chmod 0700 secrets/master 2>/dev/null || sudo chmod 0700 secrets/master
echo "done; keep a copy of secrets/master/master.key after the first server start"
