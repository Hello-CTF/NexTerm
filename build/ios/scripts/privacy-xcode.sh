#!/bin/sh
set -eu
APP_ROOT="${PROJECT_DIR}/../../.."
case "${PLATFORM_NAME}" in
  iphoneos) PRIVACY_PLATFORM=device ;;
  iphonesimulator) PRIVACY_PLATFORM=simulator ;;
  *) echo "Unsupported privacy runtime platform: ${PLATFORM_NAME}" >&2; exit 1 ;;
esac
PRIVACY_OUTPUT="${DERIVED_FILE_DIR}/nexterm-privacy-${PRIVACY_PLATFORM}"
cd "$APP_ROOT"
rm -rf "$PRIVACY_OUTPUT"
node scripts/ai-privacy-runtime.mjs --os=ios --arch=arm64 --ios-platform="$PRIVACY_PLATFORM" --output="$PRIVACY_OUTPUT"
PRIVACY_DESTINATION="${TARGET_BUILD_DIR}/${FRAMEWORKS_FOLDER_PATH}"
# Frameworks/ holds only the signed framework; runtime.json and licenses go to ai-runtime/.
PRIVACY_NOTICES="${TARGET_BUILD_DIR}/${CONTENTS_FOLDER_PATH}/ai-runtime"
mkdir -p "$PRIVACY_DESTINATION"
rm -rf "$PRIVACY_NOTICES"
mkdir -p "$PRIVACY_NOTICES"
cp -R "$PRIVACY_OUTPUT/onnxruntime.framework" "$PRIVACY_DESTINATION/"
cp "$PRIVACY_OUTPUT/runtime.json" "$PRIVACY_NOTICES/"
cp -R "$PRIVACY_OUTPUT/licenses" "$PRIVACY_NOTICES/"
if [ "${CODE_SIGNING_ALLOWED:-NO}" = YES ]; then
  PRIVACY_IDENTITY="${EXPANDED_CODE_SIGN_IDENTITY:--}"
  for framework in "$PRIVACY_DESTINATION/"*.framework; do
    codesign --force --sign "$PRIVACY_IDENTITY" --timestamp=none "$framework"
  done
fi
