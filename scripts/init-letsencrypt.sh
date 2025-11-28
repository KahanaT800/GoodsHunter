#!/bin/bash

if ! [ -x "$(command -v docker-compose)" ]; then
  if ! [ -x "$(command -v docker)" ]; then
    echo 'Error: docker is not installed.' >&2
    exit 1
  fi
  # 如果没有 docker-compose 命令，尝试使用 docker compose
  DOCKER_COMPOSE="docker compose"
else
  DOCKER_COMPOSE="docker-compose"
fi

# 加载环境变量
if [ -f .env ]; then
  export $(cat .env | grep -v '#' | awk '/=/ {print $1}')
fi

# 从环境变量获取域名 (默认为 example.com)
DOMAIN="${DOMAIN_NAME:-example.com}"
EMAIL="${CERT_EMAIL:-}"

# 自动更新 Nginx 配置文件中的域名
NGINX_CONF="./nginx/conf.d/app.conf"
if [ -f "$NGINX_CONF" ]; then
  echo "### Updating Nginx configuration with domain $DOMAIN ..."
  # 替换 server_name
  sed -i "s/server_name .*/server_name $DOMAIN www.$DOMAIN;/" "$NGINX_CONF"
  # 替换 SSL 证书路径
  sed -i "s|/etc/letsencrypt/live/[^/]*/|/etc/letsencrypt/live/$DOMAIN/|g" "$NGINX_CONF"
fi

domains=($DOMAIN www.$DOMAIN)
rsa_key_size=4096
data_path="./certbot"
staging=0 # 设置为 1 进行测试，设置为 0 生产环境

if [ -z "$EMAIL" ]; then
  echo "Error: CERT_EMAIL is not set in .env"
  echo "Please set CERT_EMAIL=your-email@example.com in .env"
  exit 1
fi

if [ -d "$data_path" ]; then
  read -p "Existing data found for $domains. Continue and replace existing certificate? (y/N) " decision
  if [ "$decision" != "Y" ] && [ "$decision" != "y" ]; then
    exit
  fi
fi

if [ ! -e "$data_path/conf/options-ssl-nginx.conf" ] || [ ! -e "$data_path/conf/ssl-dhparams.pem" ]; then
  echo "### Downloading recommended TLS parameters ..."
  mkdir -p "$data_path/conf"
  curl -s https://raw.githubusercontent.com/certbot/certbot/master/certbot-nginx/certbot_nginx/_internal/tls_configs/options-ssl-nginx.conf > "$data_path/conf/options-ssl-nginx.conf"
  curl -s https://raw.githubusercontent.com/certbot/certbot/master/certbot/certbot/ssl-dhparams.pem > "$data_path/conf/ssl-dhparams.pem"
  echo
fi

echo "### Creating dummy certificate for $domains ..."
path="/etc/letsencrypt/live/$domains"
mkdir -p "$data_path/conf/live/$domains"
$DOCKER_COMPOSE run --rm --entrypoint "\
  openssl req -x509 -nodes -newkey rsa:$rsa_key_size -days 1\
    -keyout '$path/privkey.pem' \
    -out '$path/fullchain.pem' \
    -subj '/CN=localhost'" certbot
echo

echo "### Starting nginx ..."
$DOCKER_COMPOSE up --force-recreate -d nginx
echo

echo "### Deleting dummy certificate for $domains ..."
$DOCKER_COMPOSE run --rm --entrypoint "\
  rm -Rf /etc/letsencrypt/live/$domains && \
  rm -Rf /etc/letsencrypt/archive/$domains && \
  rm -Rf /etc/letsencrypt/renewal/$domains.conf" certbot
echo

echo "### Requesting Let's Encrypt certificate for $domains ..."
# Join domains to -d args
domain_args=""
for domain in "${domains[@]}"; do
  domain_args="$domain_args -d $domain"
done

# Select appropriate email arg
case "$EMAIL" in
  "") email_arg="--register-unsafely-without-email" ;;
  *) email_arg="-m $EMAIL" ;;
esac

# Enable staging mode if needed
if [ $staging != "0" ]; then staging_arg="--staging"; fi

$DOCKER_COMPOSE run --rm --entrypoint "\
  certbot certonly --webroot -w /var/www/certbot \
    $staging_arg \
    $email_arg \
    $domain_args \
    --rsa-key-size $rsa_key_size \
    --agree-tos \
    --force-renewal" certbot
echo

echo "### Reloading nginx ..."
$DOCKER_COMPOSE exec nginx nginx -s reload
