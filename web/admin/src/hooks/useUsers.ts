import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchAllUsers, issueTempPassword } from '@/api/admin'
import type { AdminUser, TempPasswordOptions, TempPasswordResult } from '@/api/admin'

export function useAllUsers() {
  return useQuery<AdminUser[]>({
    queryKey: ['admin-users'],
    queryFn: fetchAllUsers,
    staleTime: 30_000,
  })
}

export function useIssueTempPassword() {
  const queryClient = useQueryClient()

  return useMutation<TempPasswordResult, Error, { userId: number; options?: TempPasswordOptions }>({
    mutationFn: ({ userId, options }) => issueTempPassword(userId, options),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['admin-users'] })
    },
  })
}
