import React from "react";
import { createRoot } from "react-dom/client";
import "@douyinfe/semi-ui/react19-adapter";
import "@douyinfe/semi-ui/lib/es/_base/base.css";
import { App } from "./App";
import "./styles.css";

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
