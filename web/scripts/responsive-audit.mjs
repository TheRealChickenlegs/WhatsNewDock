/**
 * Responsive layout audit.
 *
 * Drives a real browser through every page at phone, tablet and desktop widths
 * and fails when the layout regresses. It checks the things that are easy to
 * break and hard to notice:
 *
 *   - the sidebar must not eat the screen on a phone, and must be dismissible
 *   - the content column must get the full viewport width on mobile
 *   - nothing may spill out of its box, or past the right edge
 *   - the container list must be cards below md, not a sideways-scrolling table
 *   - changelog tables must scroll inside their own wrapper
 *   - interactive elements must be big enough to tap
 *   - the browser console must be clean (blocked assets, React warnings)
 *
 * Playwright is not a project dependency; install it on demand:
 *
 *   cd web
 *   npm i -D playwright && npx playwright install chromium
 *   node scripts/responsive-audit.mjs
 *
 * Environment:
 *   BASE  URL to audit (default http://[::1]:5173, the Vite dev server)
 *   TAG   prefix for the screenshots written to /tmp/wnd-ui/shots
 */
import { chromium } from 'playwright'
import fs from 'node:fs'

const BASE = process.env.BASE || 'http://[::1]:5173'
const TAG = process.env.TAG || 'before'
const SHOTS = process.env.SHOTS || '/tmp/wnd-ui/shots'
fs.mkdirSync(SHOTS, { recursive: true })

const VIEWPORTS = [
  { name: 'phone-mini', width: 320, height: 568, mobile: true },
  { name: 'phone-se', width: 375, height: 667, mobile: true },
  { name: 'phone-14pro', width: 393, height: 852, mobile: true },
  { name: 'phone-pixel', width: 412, height: 915, mobile: true },
  { name: 'phone-land', width: 844, height: 390, mobile: true },
  { name: 'tablet-mini', width: 768, height: 1024, mobile: true },
  { name: 'tablet-pro', width: 1024, height: 1366, mobile: true },
  { name: 'desktop', width: 1440, height: 900, mobile: false },
]

const PAGES = [
  { path: '/', name: 'dashboard' },
  { path: '/containers', name: 'containers' },
  { path: '/updates', name: 'updates' },
  { path: '/servers', name: 'servers' },
  { path: '/stacks', name: 'stacks' },
  { path: '/events', name: 'events' },
  { path: '/settings', name: 'settings' },
  { path: '/about', name: 'about' },
]

/**
 * Layout quality probe.
 *  - shell:  is there a way to open the nav on this viewport, does the sidebar
 *            eat the screen, how wide is the content column
 *  - clipped: elements whose own content is wider/taller than their visible box
 *            while overflow is `visible` (content spilling out of a card)
 *  - escape:  elements painted past the right edge of the viewport that are not
 *            inside an intentional scroll container
 *  - targets: interactive elements smaller than a comfortable touch target
 */
const PROBE = `(() => {
  const vw = window.innerWidth
  const px = (n) => Math.round(n)
  const desc = (el) => {
    let s = el.tagName.toLowerCase()
    if (typeof el.className === 'string' && el.className.trim()) {
      s += '.' + el.className.trim().split(/\\s+/).slice(0, 2).join('.')
    }
    return s.slice(0, 90)
  }
  const inScroller = (el) => {
    for (let p = el.parentElement; p && p !== document.body; p = p.parentElement) {
      const ox = getComputedStyle(p).overflowX
      if (ox === 'auto' || ox === 'scroll' || ox === 'hidden') return true
    }
    return false
  }

  const aside = document.querySelector('aside')
  const main = document.querySelector('main')
  const toggle = document.querySelector('[data-nav-toggle]')
  const toggleVisible = !!(toggle && toggle.getBoundingClientRect().width > 0 &&
    getComputedStyle(toggle).visibility !== 'hidden')
  const asideRect = aside ? aside.getBoundingClientRect() : null
  // 'visible' means actually painted on the screen: an off-canvas drawer has
  // a negative left and must not count as covering the page.
  const asideVisible = !!(asideRect && asideRect.width > 0 && asideRect.left >= -4 && asideRect.right > 0)

  const clipped = []
  for (const el of document.querySelectorAll('main *, aside *')) {
    const cs = getComputedStyle(el)
    if (cs.overflowX !== 'visible') continue
    if (cs.display === 'inline') continue
    const over = el.scrollWidth - el.clientWidth
    if (el.clientWidth > 0 && over > 2) {
      clipped.push({ el: desc(el), over, clientWidth: el.clientWidth, scrollWidth: el.scrollWidth })
    }
  }

  const escape = []
  for (const el of document.querySelectorAll('body *')) {
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    if (r.right <= vw + 1) continue
    if (inScroller(el)) continue
    if (getComputedStyle(el).position === 'fixed') continue
    escape.push({ el: desc(el), right: px(r.right) })
  }

  const small = []
  for (const el of document.querySelectorAll('main button, main a, aside button, aside a')) {
    const r = el.getBoundingClientRect()
    if (r.width === 0 || r.height === 0) continue
    if (r.height < 28 || r.width < 28) small.push({ el: desc(el), w: px(r.width), h: px(r.height) })
  }

  // Changelog tables are markdown and legitimately scroll inside their own
  // wrapper; only the container *list* table matters here.
  const isChangelogTable = (t) => !!t.closest('.md-body') || !!t.closest('.md-table-scroll')
  const tablesInMain = [...document.querySelectorAll('main table')]
    .filter((t) => t.getClientRects().length > 0 && !isChangelogTable(t)).length
  const scrollers = []
  for (const el of document.querySelectorAll('main *')) {
    const cs = getComputedStyle(el)
    if (cs.overflowX !== 'auto' && cs.overflowX !== 'scroll') continue
    // Code blocks and wrapped markdown tables scroll on purpose.
    if (el.tagName === 'PRE' || el.classList.contains('md-table-scroll')) continue
    if (el.scrollWidth - el.clientWidth > 2 && el.clientWidth > 0) {
      scrollers.push({ el: desc(el), over: el.scrollWidth - el.clientWidth })
    }
  }

  return {
    innerWidth: vw,
    docScrollWidth: document.documentElement.scrollWidth,
    hasNavToggle: !!toggle,
    navToggleVisible: toggleVisible,
    sidebarVisible: asideVisible,
    sidebarWidth: asideRect ? px(asideRect.width) : 0,
    sidebarRatio: asideRect ? +(asideRect.width / vw).toFixed(2) : 0,
    sidebarLeft: asideRect ? px(asideRect.left) : 0,
    mainWidth: main ? px(main.getBoundingClientRect().width) : 0,
    mainRatio: main ? +(main.getBoundingClientRect().width / vw).toFixed(2) : 0,
    clipped: clipped.slice(0, 6),
    clippedCount: clipped.length,
    escape: escape.slice(0, 6),
    escapeCount: escape.length,
    tablesInMain,
    scrollers: scrollers.slice(0, 5),
    scrollerCount: scrollers.length,
    smallTargets: small.slice(0, 6),
    smallTargetCount: small.length,
  }
})()`

const results = []
const browser = await chromium.launch()

for (const vp of VIEWPORTS) {
  const context = await browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.mobile,
    hasTouch: vp.mobile,
    deviceScaleFactor: 1,
  })
  const login = await context.request.post(`${BASE}/api/auth/login`, {
    data: { username: 'admin', password: 'admin12345' },
    headers: { 'X-Requested-With': 'whatsnewdock' },
  })
  if (!login.ok()) {
    console.error('login failed', login.status())
    process.exit(1)
  }
  const consoleIssues = []
  const page = await context.newPage()
  page.on('console', (m) => {
    if (m.type() === 'error' || m.type() === 'warning') consoleIssues.push(m.text().slice(0, 160))
  })
  page.on('pageerror', (e) => consoleIssues.push('pageerror: ' + String(e).slice(0, 160)))

  for (const p of PAGES) {
    await page.goto(`${BASE}${p.path}`, { waitUntil: 'networkidle' })
    await page.waitForTimeout(300)
    const probe = await page.evaluate(PROBE)
    results.push({ viewport: vp.name, page: p.name, ...probe })
    await page.screenshot({ path: `${SHOTS}/${TAG}-${vp.name}-${p.name}.png`, fullPage: true })
  }

  // Long changelogs (wide markdown tables, code blocks) live in the modal.
  await page.goto(`${BASE}/containers`, { waitUntil: 'networkidle' })
  await page.waitForTimeout(300)
  const rows = page.locator('[data-container-row]:visible')
  const named = page.locator('[data-container-row]:visible', { hasText: 'nginx-proxy' })
  if (await rows.count()) {
    await ((await named.count()) ? named.first() : rows.first()).click()
    await page.waitForTimeout(800)
    const opened = await page.locator('[role="dialog"]').count()
    const mdProbe = await page.evaluate(`(() => {
      const dlg = document.querySelector('[role="dialog"]')
      if (!dlg) return null
      const wrap = dlg.querySelector('.md-table-scroll')
      const tbl = dlg.querySelector('.md-body table')
      const dlgRect = dlg.getBoundingClientRect()
      return {
        hasTable: !!tbl,
        hasScrollWrapper: !!wrap,
        wrapperScrolls: wrap ? wrap.scrollWidth > wrap.clientWidth + 2 : false,
        dialogOverflowsViewport: dlgRect.right > window.innerWidth + 1 || dlgRect.left < -1,
        tableOverflowsDialog: tbl ? tbl.getBoundingClientRect().right > dlgRect.right + 1 : false,
      }
    })()`)
    const probe = await page.evaluate(PROBE)
    results.push({ viewport: vp.name, page: 'container-detail', modalOpened: opened > 0, changelog: mdProbe, ...probe })
    await page.screenshot({ path: `${SHOTS}/${TAG}-${vp.name}-container-detail.png` })
  }

  // The nav drawer (mobile/tablet only) must open and close.
  const toggleEl = page.locator('[data-nav-toggle]')
  const toggleUsable = (await toggleEl.count()) > 0 && (await toggleEl.first().isVisible())
  if (toggleUsable) {
    await page.keyboard.press('Escape')
    await page.goto(`${BASE}/`, { waitUntil: 'networkidle' })
    await toggleEl.first().click()
    await page.waitForTimeout(350)
    const open = await page.evaluate(`(() => {
      const a = document.querySelector('aside')
      const r = a.getBoundingClientRect()
      const nav = a.querySelector('nav a')
      return { left: Math.round(r.left), visible: r.left >= -4 && r.width > 0,
               navClickable: nav ? nav.getBoundingClientRect().width > 0 : false }
    })()`)
    await page.screenshot({ path: `${SHOTS}/${TAG}-${vp.name}-drawer-open.png` })
    await page.keyboard.press('Escape')
    await page.waitForTimeout(400)
    const closed = await page.evaluate(`(() => {
      const r = document.querySelector('aside').getBoundingClientRect()
      return { left: Math.round(r.left), offscreen: r.left + r.width <= 4 || r.width === 0 }
    })()`)
    results.push({ viewport: vp.name, page: 'nav-drawer', drawerOpen: open, drawerClosed: closed })
  }

  if (consoleIssues.length) {
    results.push({ viewport: vp.name, page: 'console', messages: [...new Set(consoleIssues)] })
  }
  await context.close()

  // The sign-in screen is the first thing a phone user sees.
  const anon = await browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    isMobile: vp.mobile,
    hasTouch: vp.mobile,
  })
  const loginPage = await anon.newPage()
  await loginPage.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
  await loginPage.waitForTimeout(200)
  const loginProbe = await loginPage.evaluate(PROBE)
  results.push({ viewport: vp.name, page: 'login', ...loginProbe })
  await loginPage.screenshot({ path: `${SHOTS}/${TAG}-${vp.name}-login.png`, fullPage: true })
  await anon.close()
}

await browser.close()

// ---- report ---------------------------------------------------------------
const problems = []
for (const r of results) {
  if (r.page === 'console') {
    problems.push(`${r.viewport}: browser console: ${r.messages[0]}`)
    continue
  }
  if (r.page === 'nav-drawer') {
    if (!r.drawerOpen?.visible || !r.drawerOpen?.navClickable) problems.push(`${r.viewport}/${r.page}: drawer did not open`)
    if (!r.drawerClosed?.offscreen) problems.push(`${r.viewport}/${r.page}: drawer did not close`)
    continue
  }
  if (r.page === 'container-detail' && r.changelog) {
    const c = r.changelog
    if (!c.hasTable) problems.push(`${r.viewport}/${r.page}: seeded wide changelog table did not render`)
    if (c.hasTable && !c.hasScrollWrapper) problems.push(`${r.viewport}/${r.page}: changelog table has no scroll wrapper`)
    if (c.dialogOverflowsViewport) problems.push(`${r.viewport}/${r.page}: detail dialog is wider than the viewport`)
    if (c.tableOverflowsDialog && !c.wrapperScrolls) {
      problems.push(`${r.viewport}/${r.page}: changelog table overflows the dialog and cannot scroll`)
    }
  }
  const mobile = r.innerWidth < 1024
  if (mobile && r.page !== 'login' && r.sidebarVisible && r.sidebarRatio > 0.35) {
    problems.push(`${r.viewport}/${r.page}: sidebar occupies ${Math.round(r.sidebarRatio * 100)}% of the screen and cannot be dismissed`)
  }
  if (mobile && r.page !== 'login' && !r.hasNavToggle) {
    problems.push(`${r.viewport}/${r.page}: no mobile navigation toggle`)
  }
  if (mobile && r.page !== 'login' && r.mainRatio < 0.9 && r.sidebarVisible) {
    problems.push(`${r.viewport}/${r.page}: content column only ${Math.round(r.mainRatio * 100)}% of the viewport`)
  }
  if (r.clippedCount > 0) {
    problems.push(`${r.viewport}/${r.page}: ${r.clippedCount} element(s) spilling out of their box (${r.clipped[0].el} +${r.clipped[0].over}px)`)
  }
  if (r.innerWidth < 768 && r.tablesInMain > 0) {
    problems.push(`${r.viewport}/${r.page}: ${r.tablesInMain} <table>(s) still rendered below the md breakpoint`)
  }
  if (mobile && r.scrollerCount > 0) {
    problems.push(`${r.viewport}/${r.page}: ${r.scrollerCount} horizontal scroller(s) inside main (${r.scrollers[0].el} +${r.scrollers[0].over}px)`)
  }
  if (r.escapeCount > 0) {
    problems.push(`${r.viewport}/${r.page}: ${r.escapeCount} element(s) past the right edge (${r.escape[0].el})`)
  }
}

const uniq = [...new Set(problems)]
console.log(`\n${TAG.toUpperCase()}: ${results.length} checks, ${uniq.length} distinct layout problems\n`)
for (const p of uniq.slice(0, 40)) console.log('  - ' + p)

console.log('\n--- per-viewport summary ---')
for (const vp of VIEWPORTS) {
  const rs = results.filter((r) => r.viewport === vp.name && r.page !== 'nav-drawer')
  if (!rs.length) continue
  const r0 = rs[0]
  const clipped = rs.reduce((a, r) => a + (r.clippedCount || 0), 0)
  const escape = rs.reduce((a, r) => a + (r.escapeCount || 0), 0)
  const small = rs.reduce((a, r) => a + (r.smallTargetCount || 0), 0)
  const tables = rs.reduce((a, r) => a + (r.tablesInMain || 0), 0)
  const scroll = rs.reduce((a, r) => a + (r.scrollerCount || 0), 0)
  console.log(
    `${vp.name.padEnd(12)} ${String(vp.width).padStart(4)}px  toggle=${r0.navToggleVisible ? 'yes' : 'no '}  ` +
      `sidebar=${String(r0.sidebarWidth).padStart(3)}px(${Math.round(r0.sidebarRatio * 100)}%)  ` +
      `main=${String(r0.mainWidth).padStart(4)}px(${Math.round(r0.mainRatio * 100)}%)  ` +
      `clipped=${clipped}  scroll=${scroll}  tables=${tables}  escape=${escape}  smallTargets=${small}`,
  )
}
fs.writeFileSync(`/tmp/wnd-ui/${TAG}-report.json`, JSON.stringify(results, null, 2))
