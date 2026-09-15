import {
  Github,
  Scale,
  BookOpen,
  Bug,
  Container,
  ExternalLink,
  RefreshCw,
  Server,
  ScrollText,
  ShieldCheck,
} from 'lucide-react'
import { useFetch } from '@/lib/hooks'
import { Badge, Card, CardHeader } from '@/components/ui'
import chickenlegs from '@/assets/chickenlegs.png'

const REPO = 'https://github.com/TheRealChickenlegs/WhatsNewDock'
const PROFILE = 'https://github.com/TheRealChickenlegs'

const features = [
  { icon: RefreshCw, text: 'Detects newer image versions and how far behind you are' },
  { icon: ScrollText, text: 'Aggregates changelogs from GitHub, GitLab, Gitea and registries' },
  { icon: Server, text: 'One UI for every server, stack and container, via agents' },
  { icon: ShieldCheck, text: 'Hardened, non-root, and talks to Docker through a socket proxy' },
]

const stack = ['Go', 'React', 'TypeScript', 'Tailwind CSS', 'SQLite', 'Docker Engine API']

export default function About() {
  const { data } = useFetch<{ name: string; version: string }>('/api/v1/about')

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-foreground">About</h1>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Docker updates and changelogs, in one place.
        </p>
      </div>

      {/* Author */}
      <Card className="p-6">
        <div className="flex flex-col items-center gap-6 sm:flex-row">
          <img
            src={chickenlegs}
            alt="Ryan McFadden"
            className="h-28 w-28 shrink-0 object-contain"
          />
          <div className="min-w-0 text-center sm:text-left">
            <h2 className="text-lg font-semibold text-foreground">Ryan McFadden</h2>
            <p className="mt-0.5 text-sm text-muted-foreground">Creator &amp; maintainer</p>
            <div className="mt-4 flex flex-wrap justify-center gap-2 sm:justify-start">
              <a
                href={PROFILE}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-2 rounded-lg border border-border bg-background px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-card-hover"
              >
                <Github className="h-4 w-4" />
                @TheRealChickenlegs
                <ExternalLink className="h-3 w-3 text-muted-foreground" />
              </a>
              <a
                href={REPO}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-2 rounded-lg border border-border bg-background px-3 py-1.5 text-xs font-medium text-foreground transition-colors hover:bg-card-hover"
              >
                <Container className="h-4 w-4" />
                WhatsNewDock
                <ExternalLink className="h-3 w-3 text-muted-foreground" />
              </a>
            </div>
          </div>
        </div>
      </Card>

      {/* The project */}
      <Card>
        <CardHeader
          title="The project"
          subtitle="What WhatsNewDock is and why it exists."
          action={data?.version ? <Badge variant="muted">v{data.version}</Badge> : undefined}
        />
        <div className="space-y-5 p-5">
          <p className="text-sm leading-relaxed text-muted-foreground">
            WhatsNewDock is a self-hosted monitor for Docker container updates. It watches every
            container across all of your Docker hosts, tells you when a newer image is available,
            and — most importantly — shows you{' '}
            <span className="font-medium text-foreground">what actually changed</span> by pulling
            the release notes straight from the upstream repositories. When you are several versions
            behind, you get the changelog for each version you skipped.
          </p>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            {features.map(({ icon: Icon, text }) => (
              <div key={text} className="flex items-start gap-3 rounded-lg border border-border bg-background p-3">
                <Icon className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                <span className="text-xs text-muted-foreground">{text}</span>
              </div>
            ))}
          </div>
          <div>
            <div className="mb-2 text-xs font-medium text-muted-foreground">Built with</div>
            <div className="flex flex-wrap gap-2">
              {stack.map((s) => (
                <Badge key={s} variant="default">
                  {s}
                </Badge>
              ))}
            </div>
          </div>
        </div>
      </Card>

      {/* Links */}
      <Card>
        <CardHeader title="Links & details" />
        <div className="grid grid-cols-1 gap-3 p-5 sm:grid-cols-2">
          <LinkRow href={REPO} icon={Github} label="GitHub repository" value="TheRealChickenlegs/WhatsNewDock" />
          <LinkRow href={`${REPO}/blob/main/LICENSE`} icon={Scale} label="License" value="MIT" />
          <LinkRow href={`${REPO}#readme`} icon={BookOpen} label="Documentation" value="Setup & configuration" />
          <LinkRow href={`${REPO}/issues`} icon={Bug} label="Issues & feedback" value="Report a problem" />
        </div>
      </Card>
    </div>
  )
}

function LinkRow({
  href,
  icon: Icon,
  label,
  value,
}: {
  href: string
  icon: typeof Github
  label: string
  value: string
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noreferrer"
      className="flex items-center gap-3 rounded-lg border border-border bg-background p-3 transition-colors hover:bg-card-hover"
    >
      <Icon className="h-4 w-4 shrink-0 text-primary" />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium text-foreground">{label}</div>
        <div className="truncate text-xs text-muted-foreground">{value}</div>
      </div>
      <ExternalLink className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
    </a>
  )
}
