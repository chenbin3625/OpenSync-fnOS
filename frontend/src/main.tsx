import React from "react";
import { createRoot } from "react-dom/client";
import "@douyinfe/semi-ui/react19-adapter";
import "@douyinfe/semi-ui/lib/es/_base/base.css";
import { App } from "./App";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { isMockApiMode } from "./mocks/config";
import "./styles.css";

async function bootstrap() {
  // import.meta.env.DEV gates the dynamic import so the MSW worker and its
  // ~430KB chunk are tree-shaken out of production builds entirely, instead of
  // shipping inside the go:embed payload and relying on VITE_DATA_MODE being
  // unset at runtime.
  if (import.meta.env.DEV && isMockApiMode()) {
    const { worker } = await import("./mocks/browser");
    await worker.start({
      onUnhandledRequest: "error",
      serviceWorker: {
        url: `${import.meta.env.BASE_URL}mockServiceWorker.js`,
      },
    });
  }

  createRoot(document.getElementById("root")!).render(
    <React.StrictMode>
      <ErrorBoundary>
        <App />
      </ErrorBoundary>
    </React.StrictMode>,
  );
}

void bootstrap();
