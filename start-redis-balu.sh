#!/bin/bash

echo "🚀 Iniciando entorno de REDIS balu ..."

BACKEND_DIR="/home/alejandro/Documents/BussinesInteligent/Map/dev-back-map-v1"
FRONTEND_DIR="/home/alejandro/Documents/BussinesInteligent/Map/dev-front-map-v1"

# === LIMPIEZA ===
cleanup() {
  echo ""
  echo "🛑 Deteniendo servicios..."

  [[ -n "$SSM_PID" ]] && kill $SSM_PID 2>/dev/null
  [[ -n "$BACKEND_PID" ]] && kill $BACKEND_PID 2>/dev/null
  [[ -n "$FRONTEND_PID" ]] && kill $FRONTEND_PID 2>/dev/null

  [[ -n "$TAIL1" ]] && kill $TAIL1 2>/dev/null
  [[ -n "$TAIL2" ]] && kill $TAIL2 2>/dev/null
  [[ -n "$TAIL3" ]] && kill $TAIL3 2>/dev/null

  exit 0
}

trap cleanup INT TERM EXIT

# Limpiar logs anteriores
rm -f /tmp/aws-tunnel.log /tmp/backend.log /tmp/frontend.log

# === AWS SSM ===
echo "🔌 Iniciando túnel a RDS..."
aws ssm start-session \
    --target i-074e88b2ee9d1d67f \
    --document-name AWS-StartPortForwardingSessionToRemoteHost \
    --parameters '{
        "host": ["dev-balu-redis.tgplhj.0001.use1.cache.amazonaws.com"],
        "portNumber": ["6379"],
        "localPortNumber": ["6379"]
    }' \
    --region us-east-1 \
  >/tmp/aws-tunnel.log 2>&1 &

SSM_PID=$!
sleep 3

if ! ps -p $SSM_PID >/dev/null; then
  echo "❌ El túnel AWS falló. Revisa /tmp/aws-tunnel.log"
  exit 1
fi

echo "SSM PID: $SSM_PID"

# === BACKEND ===
echo "⚙️ Iniciando backend..."
cd "$BACKEND_DIR" || exit 1
npm run start >/tmp/backend.log 2>&1 &
BACKEND_PID=$!

sleep 2
if ! ps -p $BACKEND_PID >/dev/null; then
  echo "❌ Backend falló. Revisa /tmp/backend.log"
  exit 1
fi

echo "Backend PID: $BACKEND_PID"

# === FRONTEND ===
echo "🌐 Iniciando frontend..."
cd "$FRONTEND_DIR" || exit 1
npm run dev >/tmp/frontend.log 2>&1 &
FRONTEND_PID=$!

sleep 2
if ! ps -p $FRONTEND_PID >/dev/null; then
  echo "❌ Frontend falló. Revisa /tmp/frontend.log"
  exit 1
fi

echo "Frontend PID: $FRONTEND_PID"

echo "✅ Todo levantado correctamente"
echo "📡 Mostrando logs en vivo (CTRL+C para salir)"

# === LOGS EN VIVO CON PREFIJO ===
tail -f /tmp/aws-tunnel.log | sed 's/^/[AWS] /' &
TAIL1=$!

tail -f /tmp/backend.log | sed 's/^/[BACKEND] /' &
TAIL2=$!

tail -f /tmp/frontend.log | sed 's/^/[FRONTEND] /' &
TAIL3=$!

# Mantener vivo
wait $TAIL1 $TAIL2 $TAIL3
