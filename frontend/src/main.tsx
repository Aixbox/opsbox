import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { initApiBase } from "./lib/api-client";
import { AppProviders } from "./providers";
import "./styles.css";

async function bootstrap() {
  // 先发现本地 API 端口（Wails 绑定），dev 模式走 vite 代理（相对路径）
  await initApiBase();
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <AppProviders>
        <App />
      </AppProviders>
    </StrictMode>,
  );
}

void bootstrap();
