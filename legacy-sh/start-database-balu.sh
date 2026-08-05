#!/bin/bash

echo "🚀 Iniciando túnel SSM hacia base de datos de BALU PROD..."

# Verificar si AWS CLI está instalado
if ! command -v aws &>/dev/null; then
  echo "❌ AWS CLI no está instalado. Instálalo con: sudo apt install awscli"
  exit 1
fi
https://appbi-asistencia-api.actoreselectorales.com/
# Verificar si las credenciales están configuradas
if ! aws sts get-caller-identity &>/dev/null; then
  echo "❌ Credenciales de AWS no configuradas."
  echo ""
  echo "   Opciones para configurarlas:"
  echo "   1. Ejecuta: aws configure"
  echo "   2. Copia desde Windows:"
  echo "      cp /mnt/c/Users/TuUsuario/.aws/credentials ~/.aws/credentials"
  echo "      cp /mnt/c/Users/TuUsuario/.aws/config ~/.aws/config"
  echo ""
  exit 1  
fi

echo "✅ Credenciales verificadas correctamente"
echo "🔗 Conectando al host de producción en el puerto local 5437..."
echo ""

aws ssm start-session \
    --target i-074e88b2ee9d1d67f \
    --document-name AWS-StartPortForwardingSessionToRemoteHost \
    --parameters '{
        "host": ["rds-gal-app-prod.cluster-cniu2mwmabwe.us-east-1.rds.amazonaws.com"],
        "portNumber": ["5432"],
        "localPortNumber": ["5437"]
    }' \
    --region us-east-1

# Verificar si el comando falló
if [ $? -ne 0 ]; then
  echo ""
  echo "❌ Error al iniciar la sesión SSM."
  echo "   Verifica que la instancia esté activa y tengas permisos SSM."
  exit 1
fi

echo ""
echo "✅ Túnel cerrado correctamente."
