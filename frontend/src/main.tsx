import React from "react";
import { createRoot } from "react-dom/client";
import "@douyinfe/semi-ui/react19-adapter";
import "@douyinfe/semi-ui/lib/es/_base/base.css";
import { App } from "./App";
import { isMockApiMode } from "./mocks/config";
import "./styles.css";

async function bootstrap() {
  if (isMockApiMode()) {
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
      <App />
    </React.StrictMode>,
  );
}

void bootstrap();
