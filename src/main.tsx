import { initI18n } from "./i18n";

// The interface language's catalogs load before any interface module, so text a module builds as it loads is
// already translated.
void initI18n().finally(() => import("./appMain"));
