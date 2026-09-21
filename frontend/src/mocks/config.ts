type MockEnv = {
  VITE_DATA_MODE?: string;
};

export function isMockApiMode(
  env: MockEnv = import.meta.env as unknown as MockEnv,
): boolean {
  return env.VITE_DATA_MODE === "mock";
}
