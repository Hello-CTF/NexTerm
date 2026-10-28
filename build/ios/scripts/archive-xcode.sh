#!/bin/sh
set -eu
APP_ROOT="${PROJECT_DIR}/../../.."
case "${PLATFORM_NAME}" in
  iphoneos) IOS_PLATFORM=device ;;
  iphonesimulator) IOS_PLATFORM=simulator ;;
  *) echo "Unsupported iOS platform: ${PLATFORM_NAME}" >&2; exit 1 ;;
esac
cd "$APP_ROOT"
task ios:build IOS_PLATFORM="$IOS_PLATFORM" ARCH=arm64 OUTPUT="${APP_ROOT}/bin/${PRODUCT_NAME}" PRODUCTION=true
