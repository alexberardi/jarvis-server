import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

/**
 * Markdown renders assistant text, inbox bodies and tool output (webapp 01 G14). Raw HTML is
 * dropped (`skipHtml`), never rendered; react-markdown's default URL filter removes
 * `javascript:` and other unsafe links; links open in a new tab without an opener; images are
 * shown as links instead of being fetched (the CSP would block a remote image anyway). The
 * CSP (`script-src 'self'`) is the backstop.
 */
const components: Components = {
  a: ({ href, children }) => (
    <a href={href} target="_blank" rel="noopener noreferrer nofollow">
      {children}
    </a>
  ),
  img: ({ src, alt }) =>
    typeof src === 'string' && src ? (
      <a href={src} target="_blank" rel="noopener noreferrer nofollow">
        {alt || 'image'}
      </a>
    ) : (
      <>{alt}</>
    ),
}

export default function Markdown({ children }: { children: string }) {
  return (
    <div className="md-body">
      <ReactMarkdown remarkPlugins={[remarkGfm]} skipHtml components={components}>
        {children}
      </ReactMarkdown>
    </div>
  )
}
