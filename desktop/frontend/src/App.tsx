import { AppShell } from "./components/AppShell";
import { AppStateProvider } from "./state/store";

function App() {
  return (
    <AppStateProvider>
      <AppShell />
    </AppStateProvider>
  );
}

export default App;
