import React from "react";

type Props = { children: React.ReactNode };
type State = { error: Error | null };

/**
 * Catches render-time exceptions. Without it any thrown error unmounts the whole
 * tree and leaves a blank page with no way back — on a NAS appliance that looks
 * like the app died, so the fallback offers an explicit reload.
 */
export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error("界面渲染失败", error, info.componentStack);
  }

  render() {
    const { error } = this.state;
    if (!error) return this.props.children;
    return (
      <div className="app-error-boundary" role="alert">
        <h2>界面出错了</h2>
        <p>
          页面渲染时发生异常，同步任务本身不受影响，仍在后台按计划运行。
        </p>
        <pre>{error.message}</pre>
        <button type="button" onClick={() => window.location.reload()}>
          重新加载
        </button>
      </div>
    );
  }
}
