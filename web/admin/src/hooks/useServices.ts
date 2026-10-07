import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  getServiceRegistry,
  registerServices,
  rotateServiceKey,
  addService,
  deleteService,
  getServiceSuggestions,
} from '@/api/services'
import type {
  ServiceRegisterRequest,
  ServiceRegisterResponse,
  KeyRotateRequest,
  KeyRotateResponse,
  AddServiceRequest,
} from '@/types/services'

export function useServiceRegistry() {
  return useQuery({
    queryKey: ['service-registry'],
    queryFn: getServiceRegistry,
    staleTime: 30_000,
  })
}

export function useRegisterServices() {
  const queryClient = useQueryClient()

  return useMutation<ServiceRegisterResponse, Error, ServiceRegisterRequest>({
    mutationFn: registerServices,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-registry'] })
    },
  })
}

export function useRotateKey() {
  const queryClient = useQueryClient()

  return useMutation<KeyRotateResponse, Error, KeyRotateRequest>({
    mutationFn: rotateServiceKey,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-registry'] })
    },
  })
}

export function useAddService() {
  const queryClient = useQueryClient()

  return useMutation<ServiceRegisterResponse, Error, AddServiceRequest>({
    mutationFn: addService,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-registry'] })
    },
  })
}

export function useDeleteService() {
  const queryClient = useQueryClient()

  return useMutation<void, Error, string>({
    mutationFn: deleteService,
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['service-registry'] })
    },
  })
}

export function useServiceSuggestions() {
  return useQuery({
    queryKey: ['service-suggestions'],
    queryFn: getServiceSuggestions,
    staleTime: 60_000,
  })
}

