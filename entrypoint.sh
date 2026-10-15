#!/bin/sh
# 容器入口。工作目录 /opt/payprint，二进制旁的两样东西这样落到挂载卷里：
#   - payprint.conf 是指向 data/payprint.conf 的软链接：首次启动它不存在，服务按内置模板
#     生成配置时会顺着链接写进卷里。路径解析不跟随符号链接（config.resolvePath 只做
#     filepath.Abs），所以 db_path / storage_dir 仍以 /opt/payprint 为基准，
#     正好落在挂载的 data/ 里。
#   - data/ 本身就是挂载点。备份它 = 备份整站（配置、数据库、上传的文件）。

umask ${UMASK}

if [ -d ./data ] && { [ ! -w ./data ] || [ ! -x ./data ]; }; then
  cat <<EOF
错误：当前用户（uid $(id -u)）对 ./data 目录（$(pwd)/data）没有写/执行权限。
通常是挂载目录的属主与容器用户（uid 1001）不一致，请在宿主机上修正目录权限后重试。
EOF
  exit 1
fi

if [ ! -e ./payprint.conf ] && [ ! -L ./payprint.conf ]; then
  ln -s data/payprint.conf ./payprint.conf
fi

exec ./payprint-server "$@"
