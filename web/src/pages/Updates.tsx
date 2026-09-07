import ContainerExplorer from '@/components/ContainerExplorer'

export default function Updates() {
  return (
    <ContainerExplorer
      title="Updates"
      subtitle="Every container with a newer image available, with its changelog."
      alwaysUpdates
    />
  )
}
