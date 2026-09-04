import { CreateKeyDialog } from "@/components/CreateKeyDialog";
import { ErrorBanner } from "@/components/ErrorBanner";
import { KeyCreatedDialog } from "@/components/KeyCreatedDialog";
import { KeysCard } from "@/components/KeysCard";
import { RevokeKeyDialog } from "@/components/RevokeKeyDialog";
import { Topbar } from "@/components/Topbar";

export default function App() {
  return (
    <div className="min-h-screen bg-canvas text-foreground">
      <Topbar />

      <main className="flex w-full flex-col gap-6 px-10 py-8">
        <header className="flex flex-col gap-1">
          <h1 className="font-semibold text-2xl tracking-tight">LLM API Key Manager</h1>
          <p className="text-muted-foreground text-sm">
            View and manage your API keys. Do not share your API keys with others or expose them in
            the browser or shared repositories.
          </p>
        </header>

        <ErrorBanner />

        <KeysCard className="pt-4" />
      </main>

      <CreateKeyDialog />
      <KeyCreatedDialog />
      <RevokeKeyDialog />
    </div>
  );
}
