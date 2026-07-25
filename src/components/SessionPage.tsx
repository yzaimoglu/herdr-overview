import { useEffect, useState, type FormEvent } from 'react';
import { Badge } from '@cloudflare/kumo/components/badge';
import { Banner } from '@cloudflare/kumo/components/banner';
import { Button } from '@cloudflare/kumo/components/button';
import { CodeBlock } from '@cloudflare/kumo/components/code';
import { Empty } from '@cloudflare/kumo/components/empty';
import { Textarea } from '@cloudflare/kumo/components/input';
import { LayerCard } from '@cloudflare/kumo/components/layer-card';
import { Loader } from '@cloudflare/kumo/components/loader';
import { Text } from '@cloudflare/kumo/components/text';
import {
  ArrowLeft,
  ArrowsClockwise,
  Crosshair,
  PaperPlaneTilt,
  WarningCircle,
  X,
} from '@phosphor-icons/react';

type AgentStatus = 'working' | 'blocked' | 'idle' | 'done' | 'unknown';

type Agent = {
  name: string;
  agent: string;
  status: AgentStatus;
  workspaceId: string;
  paneId: string;
  terminalId: string;
  sessionId: string;
  cwd: string;
  focused: boolean;
};

type Overview = { agents: Agent[] };
type ChatMessage = { id: number; text: string };

const apiBase = import.meta.env.PUBLIC_API_BASE ?? '';

async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${apiBase}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  const body = (await response.json()) as T & { error?: string };
  if (!response.ok) throw new Error(body.error ?? `Request failed with status ${response.status}`);
  return body;
}

function statusLabel(status: AgentStatus) {
  return ({ working: 'Working', blocked: 'Needs input', idle: 'Ready', done: 'Done', unknown: 'Unknown' })[status];
}

function statusVariant(status: AgentStatus) {
  if (status === 'working') return 'info' as const;
  if (status === 'blocked') return 'warning' as const;
  if (status === 'done') return 'success' as const;
  return 'secondary' as const;
}

function logoSource(tool: string) {
  return {
    claude: '/agent-icons/anthropic.svg',
    codex: '/agent-icons/openai.svg',
    opencode: '/agent-icons/opencode.svg',
  }[tool.toLowerCase()];
}

function shortWorkspace(value: string) {
  const parts = value.split('/').filter(Boolean);
  return parts.length > 2 ? parts.slice(-2).join('/') : value;
}

function timeAgo(timestamp: Date | null) {
  if (!timestamp) return 'Not refreshed yet';
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp.getTime()) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  return `${Math.floor(seconds / 60)}m ago`;
}

export default function SessionPage({ paneId }: { paneId: string }) {
  const [agent, setAgent] = useState<Agent | null>(null);
  const [output, setOutput] = useState('');
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [prompt, setPrompt] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [sending, setSending] = useState(false);
  const [lastRefreshed, setLastRefreshed] = useState<Date | null>(null);

  async function refreshOutput() {
    setRefreshing(true);
    try {
      const result = await api<{ text: string }>(`/api/agents/${encodeURIComponent(paneId)}/output?lines=200`);
      setOutput(result.text);
      setLastRefreshed(new Date());
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not refresh this session.');
    } finally {
      setRefreshing(false);
    }
  }

  async function loadSession() {
    setLoading(true);
    try {
      const [overview, result] = await Promise.all([
        api<Overview>('/api/overview'),
        api<{ text: string }>(`/api/agents/${encodeURIComponent(paneId)}/output?lines=200`),
      ]);
      const current = overview.agents.find((item) => item.paneId === paneId);
      if (!current) throw new Error(`Session ${paneId} no longer exists.`);
      setAgent(current);
      setOutput(result.text);
      setLastRefreshed(new Date());
      setError('');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not load this session.');
    } finally {
      setLoading(false);
    }
  }

  async function sendPrompt(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!prompt.trim()) return;
    const nextPrompt = prompt.trim();
    setSending(true);
    try {
      await api(`/api/agents/${encodeURIComponent(paneId)}/prompt`, {
        method: 'POST',
        body: JSON.stringify({ prompt: nextPrompt }),
      });
      setMessages((current) => [...current, { id: Date.now(), text: nextPrompt }]);
      setPrompt('');
      await refreshOutput();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Could not send the prompt.');
    } finally {
      setSending(false);
    }
  }

  async function runAction(action: 'interrupt' | 'close') {
    if (action === 'close' && !window.confirm(`Close the ${agent?.name || agent?.agent || 'session'} pane?`)) return;
    try {
      await api(`/api/agents/${encodeURIComponent(paneId)}/${action}`, { method: 'POST' });
      if (action === 'close') window.location.assign('/');
      else await loadSession();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : `Could not ${action} the session.`);
    }
  }

  useEffect(() => {
    void loadSession();
    const interval = window.setInterval(() => void refreshOutput(), 15000);
    return () => window.clearInterval(interval);
  }, [paneId]);

  return (
    <div className="flex min-h-screen flex-col bg-kumo-canvas">
      <header className="border-b border-kumo-hairline bg-kumo-canvas/90 backdrop-blur-md">
        <div className="session-page-header-inner mx-auto flex min-h-16 w-full max-w-[1600px] items-center justify-between gap-4 px-4 md:px-8">
          <div className="flex min-w-0 items-center gap-3">
            <Button aria-label="Back to overview" shape="square" size="sm" variant="ghost" onClick={() => window.location.assign('/')}><ArrowLeft size={18} /></Button>
            <div className="flex min-w-0 items-center gap-3">
              {agent && logoSource(agent.agent) && <div className="grid size-9 shrink-0 place-items-center rounded-full bg-kumo-info-tint p-2 ring-1 ring-kumo-line"><img className="size-5 object-contain" src={logoSource(agent.agent)} alt="" aria-hidden="true" /></div>}
              <div className="flex min-w-0 flex-col gap-1">
                <Text bold size="sm" truncate>{agent?.name || agent?.agent || paneId}</Text>
                <Text variant="secondary" size="xs" truncate>Session management</Text>
              </div>
            </div>
          </div>
          <div className="flex items-center gap-3">
            <Text variant="secondary" size="xs" DANGEROUS_className="hidden sm:inline-block">Auto-refresh every 15s</Text>
            <Button aria-label="Refresh session output" shape="square" size="sm" variant="secondary" loading={refreshing} onClick={() => void refreshOutput()}><ArrowsClockwise size={17} /></Button>
          </div>
        </div>
      </header>

      <main className="session-page-main mx-auto grid min-h-0 w-full max-w-[1600px] flex-1 grid-cols-1 gap-6 p-4 md:p-8">
        <section className="session-page-chat flex min-w-0 flex-col overflow-hidden rounded-xl border border-kumo-line bg-kumo-base shadow-sm">
          <div className="flex items-center justify-between gap-3 border-b border-kumo-hairline px-4 py-4 md:px-6">
            <div className="flex min-w-0 flex-col gap-1">
              <Text variant="heading3" as="h1">Live session</Text>
              <Text variant="secondary" size="xs" truncate>{agent?.paneId || paneId} &middot; Latest output from Herdr</Text>
            </div>
            <Badge variant={agent ? statusVariant(agent.status) : 'secondary'} appearance="dot">{agent ? statusLabel(agent.status) : 'Loading'}</Badge>
          </div>

          {error && <div className="p-4 pb-0"><Banner variant="error" icon={<WarningCircle size={20} />} title="Session error" description={error} action={<Button shape="square" size="sm" variant="ghost" aria-label="Dismiss error" onClick={() => setError('')}><X size={16} /></Button>} /></div>}

          <div className="session-chat-scroll min-h-0 flex-1 space-y-4 overflow-y-auto p-4 md:p-6">
            {loading ? <div className="flex min-h-80 items-center justify-center"><Loader aria-label="Loading session" /></div> : (
              <>
                {messages.map((message) => (
                  <div className="flex justify-end" key={message.id}>
                    <div className="max-w-[min(80%,_680px)] rounded-2xl rounded-br-md bg-kumo-brand px-4 py-3 text-white shadow-sm">
                      <span className="text-sm text-white">{message.text}</span>
                    </div>
                  </div>
                ))}
                <div className="flex items-start gap-3">
                  <div className="grid size-9 shrink-0 place-items-center rounded-full bg-kumo-info-tint p-2 text-kumo-info ring-1 ring-kumo-line">{agent && logoSource(agent.agent) ? <img className="size-5 object-contain" src={logoSource(agent.agent)} alt="" aria-hidden="true" /> : <Crosshair size={17} />}</div>
                  <div className="min-w-0 flex-1 rounded-2xl rounded-tl-md border border-kumo-line bg-kumo-tint p-4">
                    <div className="mb-3 flex items-center justify-between gap-3">
                      <Text bold size="sm">Latest session output</Text>
                      <Text variant="secondary" size="xs">Updated {timeAgo(lastRefreshed)}</Text>
                    </div>
                    {output ? <CodeBlock code={output} lang="bash" /> : <Empty size="sm" title="No output yet" description="The session has not produced terminal output." />}
                  </div>
                </div>
              </>
            )}
          </div>

          <form className="border-t border-kumo-hairline bg-kumo-base p-4 md:p-6" onSubmit={sendPrompt}>
            <Textarea label="Message this agent" placeholder="Send a prompt to this session…" autoResize minRows={2} maxRows={6} value={prompt} onChange={(event) => setPrompt(event.target.value)} />
            <div className="session-composer-actions flex items-center justify-between gap-3">
              <Text variant="secondary" size="xs" DANGEROUS_className="hidden sm:inline-block">Prompts are sent to the active pane.</Text>
              <Button type="submit" variant="primary" loading={sending} icon={PaperPlaneTilt}>Send prompt</Button>
            </div>
          </form>
        </section>

        <aside className="session-side-panel flex min-w-0 flex-col gap-4 lg:sticky lg:top-6 lg:self-start">
          <LayerCard className="p-5">
            <div className="flex flex-col gap-5">
              <div className="flex flex-col gap-1">
                <Text variant="heading3" as="h2">Session panel</Text>
                <Text variant="secondary" size="xs">Runtime information and controls.</Text>
              </div>
              {agent ? <dl className="flex flex-col gap-4">
                <div><Text variant="secondary" size="xs" as="dt">Agent</Text><Text bold size="sm" as="dd">{agent.agent}</Text></div>
                <div><Text variant="secondary" size="xs" as="dt">Pane</Text><Text variant="mono-secondary" as="dd">{agent.paneId}</Text></div>
                <div><Text variant="secondary" size="xs" as="dt">Session ID</Text><Text variant="mono-secondary" as="dd" truncate>{agent.sessionId || 'Not reported'}</Text></div>
                <div><Text variant="secondary" size="xs" as="dt">Workspace</Text><Text variant="mono-secondary" as="dd" truncate>{shortWorkspace(agent.cwd || agent.workspaceId)}</Text></div>
                <div><Text variant="secondary" size="xs" as="dt">Terminal</Text><Text variant="mono-secondary" as="dd">{agent.terminalId || 'Not reported'}</Text></div>
                <div><Text variant="secondary" size="xs" as="dt">Focus</Text><Text size="sm" as="dd">{agent.focused ? 'Focused pane' : 'Background pane'}</Text></div>
              </dl> : <Loader aria-label="Loading session details" />}
            </div>
          </LayerCard>

          <LayerCard className="p-5">
            <div className="flex flex-col gap-3">
              <Text variant="heading3" as="h2">Actions</Text>
              <Button variant="outline" icon={ArrowsClockwise} loading={refreshing} onClick={() => void refreshOutput()}>Refresh output</Button>
              <Button variant="outline" icon={Crosshair} onClick={() => void runAction('interrupt')}>Interrupt agent</Button>
              <Button variant="secondary-destructive" icon={X} onClick={() => void runAction('close')}>Close pane</Button>
            </div>
          </LayerCard>
        </aside>
      </main>
    </div>
  );
}
